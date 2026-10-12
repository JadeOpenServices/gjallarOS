package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
	"github.com/JadeOpenServices/gjallarOS/internal/oddccli"
)

func TestRebuildDoesNotRequireExplicitRepoAndHost(t *testing.T) {
	t.Setenv("GJALLAROS_REPO", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	oldSystem := installercheck.SystemRepositoryFile
	installercheck.SystemRepositoryFile = filepath.Join(t.TempDir(), "repository")
	t.Cleanup(func() { installercheck.SystemRepositoryFile = oldSystem })

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	status := runRebuild(nil, &stdout, &stderr)

	if status != 2 {
		t.Fatalf(
			"runRebuild status = %d, want 2 for unresolved repository",
			status,
		)
	}

	if strings.Contains(
		stderr.String(),
		"rebuild requires --repo and --host",
	) {
		t.Fatalf(
			"rebuild still requires explicit repo/host:\n%s",
			stderr.String(),
		)
	}

	if !strings.Contains(
		stderr.String(),
		"resolve GjallarOS repository",
	) {
		t.Fatalf(
			"rebuild did not reach repository resolution:\n%s",
			stderr.String(),
		)
	}
}

func TestRebuildPreflightSuccessIsCompact(t *testing.T) {
	findings := []installercheck.Finding{
		{
			Level:   "PASS",
			Message: "default preset is valid",
		},
		{
			Level:   installercheck.Warn,
			Message: "known optional warning",
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	status := reportRebuildPreflightResult(
		findings,
		false,
		&stdout,
		&stderr,
	)

	if status != 0 {
		t.Fatalf("status = %d, want 0", status)
	}

	got := stdout.String()

	if !strings.Contains(got, "[GjallarOS] Preflight ✓") {
		t.Fatalf("compact success line missing:\n%s", got)
	}

	if !strings.Contains(got, "WARN: known optional warning") {
		t.Fatalf("warning was hidden:\n%s", got)
	}

	if strings.Contains(got, "PASS: default preset is valid") {
		t.Fatalf("successful finding leaked into rebuild output:\n%s", got)
	}

	if strings.Contains(got, "Fast preflight") {
		t.Fatalf("verbose preflight heading leaked into rebuild:\n%s", got)
	}

	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr:\n%s", stderr.String())
	}
}

func TestRebuildPreflightFailureKeepsDiagnostics(t *testing.T) {
	findings := []installercheck.Finding{
		{
			Level:   "PASS",
			Message: "good check",
		},
		{
			Level:   installercheck.Warn,
			Message: "warning detail",
		},
		{
			Level:   "FAIL",
			Message: "failure detail",
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	status := reportRebuildPreflightResult(
		findings,
		true,
		&stdout,
		&stderr,
	)

	if status != 1 {
		t.Fatalf("status = %d, want 1", status)
	}

	got := stdout.String()

	if strings.Contains(got, "PASS: good check") {
		t.Fatalf("PASS noise leaked into failed rebuild:\n%s", got)
	}

	if !strings.Contains(got, "WARN: warning detail") {
		t.Fatalf("warning detail missing:\n%s", got)
	}

	if !strings.Contains(got, "FAIL: failure detail") {
		t.Fatalf("failure detail missing:\n%s", got)
	}

	if !strings.Contains(
		stderr.String(),
		"FAIL: GjallarOS preflight failed",
	) {
		t.Fatalf(
			"failure summary missing:\n%s",
			stderr.String(),
		)
	}
}

func TestNormalizeRebuildUserIntentMigratesBlankDotfilesDir(t *testing.T) {
	user := config.User{}

	changed, err := normalizeRebuildUserIntent(&user, "/home/test/gjallarOS")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("blank dotfilesDir was not migrated")
	}
	if user.DotfilesDir != "/home/test/gjallarOS" {
		t.Fatalf("dotfilesDir=%q", user.DotfilesDir)
	}
}

func TestNormalizeRebuildUserIntentPreservesConfiguredDotfilesDir(t *testing.T) {
	user := config.User{DotfilesDir: "/srv/gjallarOS"}

	changed, err := normalizeRebuildUserIntent(&user, "/home/test/gjallarOS")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("configured dotfilesDir was unexpectedly changed")
	}
	if user.DotfilesDir != "/srv/gjallarOS" {
		t.Fatalf("dotfilesDir=%q", user.DotfilesDir)
	}
}

func TestRebuildPlanIsPureWithoutPalette(t *testing.T) {
	plan := newRebuildPlan(
		"/repo",
		"host",
		t.TempDir(),
		false,
		[]string{"--show-trace-extra"},
	)

	if got := countString(plan.eval, "--impure"); got != 0 {
		t.Fatalf("--impure count = %d, want 0: %v", got, plan.eval)
	}
	if got := countString(plan.eval, "--show-trace-extra"); got != 1 {
		t.Fatalf("forwarded rebuild arg count = %d, want 1: %v", got, plan.eval)
	}
	if plan.eval[0] != "nix" {
		t.Fatalf("eval runs %q, want nix: %v", plan.eval[0], plan.eval)
	}
}

func TestRebuildPlanAddsOneImpureForNoctaliaPalette(t *testing.T) {
	home := t.TempDir()
	palette := filepath.Join(
		home,
		".local",
		"state",
		"noctalia",
		"stylix-override.json",
	)
	if err := os.MkdirAll(filepath.Dir(palette), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(palette, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := newRebuildPlan("/repo", "host", home, true, nil)

	if got := countString(plan.eval, "--impure"); got != 1 {
		t.Fatalf("--impure count = %d, want 1: %v", got, plan.eval)
	}
	if got := countString(plan.eval, "--show-trace"); got != 1 {
		t.Fatalf("--show-trace count = %d, want 1: %v", got, plan.eval)
	}
	if len(plan.eval) < 2 || plan.eval[0] != "env" || !strings.HasPrefix(plan.eval[1], "GJALLAR_NOCTALIA_PALETTE=") {
		t.Fatalf("palette environment prefix missing: %v", plan.eval)
	}
	if countString(plan.build, "--impure") != 0 || plan.build[0] != "nix" {
		t.Fatalf("build carries the palette input: %v", plan.build)
	}
}

// The evaluation must not run in the process that builds: that process
// kept the evaluator's heap through every compile and ran a 4 GiB machine
// out of memory.
func TestRebuildPlanEvaluatesBeforeItBuilds(t *testing.T) {
	plan := newRebuildPlan("/repo", "book", "", false, []string{
		"--option", "substituters", "http://10.0.2.2:8501",
		"--install-bootloader",
		"--specialisation", "dock",
		"-L",
	})

	wantEval := []string{
		"nix", "--extra-experimental-features", "nix-command flakes",
		"eval", "--raw",
		`path:/repo#nixosConfigurations."book".config.system.build.toplevel.drvPath`,
		"--option", "substituters", "http://10.0.2.2:8501", "-L",
	}
	if !slices.Equal(plan.eval, wantEval) {
		t.Fatalf("eval = %q\nwant %q", plan.eval, wantEval)
	}
	wantBuild := []string{
		"nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths",
		"--option", "substituters", "http://10.0.2.2:8501", "-L",
	}
	if !slices.Equal(plan.build, wantBuild) {
		t.Fatalf("build = %q\nwant %q", plan.build, wantBuild)
	}
	wantSwitch := []string{"nixos-rebuild", "switch", "--no-reexec", "--install-bootloader", "--specialisation", "dock"}
	if !slices.Equal(plan.activate, wantSwitch) {
		t.Fatalf("switch = %q\nwant %q", plan.activate, wantSwitch)
	}
	for _, arg := range plan.activate {
		if strings.HasPrefix(arg, "path:") || arg == "--flake" {
			t.Fatalf("switch evaluates the flake again: %v", plan.activate)
		}
	}
}

func TestRebuildLocalDerivationsSummarizesUniqueBuilds(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "rebuild-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	input := strings.Join([]string{
		"these 3 derivations will be built:",
		"building '/nix/store/aaaaaaaa-noctalia-5.0.0.drv'...",
		"building '/nix/store/bbbbbbbb-home-manager-generation.drv'...",
		"building '/nix/store/aaaaaaaa-noctalia-5.0.0.drv'...",
		"copying path '/nix/store/cccccccc-not-a-local-build' from cache...",
	}, "\n")
	if _, err := log.WriteString(input); err != nil {
		t.Fatal(err)
	}

	got := rebuildLocalDerivations(log)
	want := []string{"noctalia-5.0.0", "home-manager-generation"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("builds = %v, want %v", got, want)
	}
}

func TestRebuildNetworkErrorFindsUnreachableHosts(t *testing.T) {
	// Output of nix 2.x and git for an unresolvable and a refused host.
	retried := []string{
		"warning: unable to download 'https://gjallar-test.invalid/x': Could not resolve hostname (6) Could not resolve host: gjallar-test.invalid; retrying in 332 ms (attempt 1/5)",
		"warning: unable to download 'http://127.0.0.1:9/x': Could not connect to server (7) Failed to connect to 127.0.0.1:9 after 0 ms: Could not connect to server; retrying in 260 ms (attempt 1/5)",
	}
	fatal := []string{
		"error: unable to download 'https://gjallar-test.invalid/x': Could not resolve hostname (6) Could not resolve host: gjallar-test.invalid",
		"fatal: unable to access 'https://gjallar-test.invalid/x/': Could not resolve host: gjallar-test.invalid",
	}
	for _, line := range append(retried, fatal...) {
		log := strings.NewReader("building '/nix/store/aaaaaaaa-etc.drv'...\n" + line + "\n")
		if got := rebuildNetworkError(log, true); got != line {
			t.Fatalf("network error = %q, want %q", got, line)
		}
	}
	for _, line := range fatal {
		log := strings.NewReader(retried[1] + "\n" + line + "\n")
		if got := rebuildNetworkError(log, false); got != line {
			t.Fatalf("network error = %q, want %q", got, line)
		}
	}

	// A retried download is not why a build failed (fw13 VM, 2026-10-09).
	log := strings.NewReader(retried[1] + "\nerror: builder for '/nix/store/aaaaaaaa-etc.drv' failed with exit code 1\n")
	if got := rebuildNetworkError(log, false); got != "" {
		t.Fatalf("network error = %q, want none", got)
	}

	log = strings.NewReader("building '/nix/store/aaaaaaaa-etc.drv'...\n")
	if got := rebuildNetworkError(log, true); got != "" {
		t.Fatalf("network error = %q, want none", got)
	}
}

func TestSystemGenerationCleanupArgsUseCountRetention(t *testing.T) {
	got := systemGenerationCleanupArgs(5)
	want := []string{
		"nix-env",
		"--profile",
		"/nix/var/nix/profiles/system",
		"--delete-generations",
		"+5",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cleanup args = %v, want %v", got, want)
	}
}

func TestParseCleanupKeepRejectsZeroAndGarbage(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "five"} {
		if _, err := parseCleanupKeep(value); err == nil {
			t.Fatalf("parseCleanupKeep(%q) unexpectedly succeeded", value)
		}
	}

	got, err := parseCleanupKeep("7")
	if err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Fatalf("keep = %d, want 7", got)
	}
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func TestRebuildHardwareUpdateFailureStopsRebuild(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	for _, marker := range []string{"flake.nix", "scripts/installation/install.sh"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, marker)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "log")
	fake := filepath.Join(t.TempDir(), "oddc")
	body := "#!/bin/sh\necho \"$@\" > " + log + "\nexit 3\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := oddccli.Path
	oddccli.Path = fake
	t.Cleanup(func() { oddccli.Path = old })

	var stdout, stderr bytes.Buffer
	status := runRebuild(
		[]string{"--repo", repo, "--hardware-update", "--channel", "staging"},
		&stdout,
		&stderr,
	)

	if status != 3 {
		t.Fatalf("status = %d, want 3:\n%s", status, stderr.String())
	}
	ran, _ := os.ReadFile(log)
	if want := "update --flake " + repo + " --channel staging\n"; string(ran) != want {
		t.Fatalf("oddc ran %q, want %q", ran, want)
	}
	if !strings.Contains(stderr.String(), "nothing rebuilt") {
		t.Fatalf("failure not reported:\n%s", stderr.String())
	}
}

func TestRebuildChannelRequiresHardwareUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := runRebuild([]string{"--repo", t.TempDir(), "--channel", "stable"}, &stdout, &stderr); status != 2 {
		t.Fatalf("status = %d, want 2", status)
	}
	if status := runRebuild([]string{"--hardware-update", "--channel", "dev"}, &stdout, &stderr); status != 2 {
		t.Fatalf("bad channel status = %d, want 2", status)
	}
	stderr.Reset()
	if status := runRebuild([]string{"--hardware-update", "--stage", "main"}, &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "--channel stable|staging") {
		t.Fatalf("old --stage: status = %d, stderr %q, want 2 and a pointer to --channel", status, stderr.String())
	}
	if status := runRebuild([]string{"--repo", t.TempDir(), "--switch", "0"}, &stdout, &stderr); status != 2 {
		t.Fatalf("switch without hardware-update status = %d, want 2", status)
	}
	if status := runRebuild([]string{"--hardware-update", "--switch", "yes"}, &stdout, &stderr); status != 2 {
		t.Fatalf("bad switch status = %d, want 2", status)
	}
}

func TestRebuildHardwareUpdateSwitchZeroOnlyMovesPin(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	for _, marker := range []string{"flake.nix", "scripts/installation/install.sh"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, marker)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "log")
	fake := filepath.Join(t.TempDir(), "oddc")
	body := "#!/bin/sh\necho \"$@\" > " + log + "\n"
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := oddccli.Path
	oddccli.Path = fake
	t.Cleanup(func() { oddccli.Path = old })

	var stdout, stderr bytes.Buffer
	status := runRebuild(
		[]string{"--repo", repo, "--hardware-update", "--channel", "stable", "--switch", "0"},
		&stdout,
		&stderr,
	)

	if status != 0 {
		t.Fatalf("status = %d, want 0:\n%s", status, stderr.String())
	}
	ran, _ := os.ReadFile(log)
	if want := "update --flake " + repo + " --channel stable\n"; string(ran) != want {
		t.Fatalf("oddc ran %q, want %q", ran, want)
	}
	if !strings.Contains(stdout.String(), "not switched") {
		t.Fatalf("no-switch not reported:\n%s", stdout.String())
	}
}
