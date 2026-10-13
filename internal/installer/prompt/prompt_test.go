package prompt

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestTerminalDefaults(t *testing.T) {
	var out strings.Builder
	u := UI{Reader: bufio.NewReader(strings.NewReader("\n\n")), Out: &out}
	yes, err := u.Confirm(context.Background(), "Continue?", true)
	if err != nil || !yes {
		t.Fatal(yes, err)
	}
	value, err := u.Value(context.Background(), "Name", "default")
	if err != nil || value != "default" {
		t.Fatal(value, err)
	}
}

func TestTerminalSecureBootRecoveryShowsBothRequiredItems(t *testing.T) {
	var out strings.Builder
	u := UI{Reader: bufio.NewReader(strings.NewReader("")), Out: &out}
	archive := "/var/lib/gjallarOS/recovery/secure-boot-keys.tar.enc"
	passphrase := "test-recovery-passphrase"
	if err := u.ShowSecureBootRecovery(context.Background(), archive, passphrase); err != nil {
		t.Fatal(err)
	}
	shown := out.String()
	if !strings.Contains(shown, archive) || !strings.Contains(shown, passphrase) {
		t.Fatalf("recovery dialog omitted required material: %q", shown)
	}
}

func TestExactConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  string
		wantError bool
	}{
		{
			name:     "exact",
			input:    "ERASE /dev/nvme1n1\n",
			expected: "ERASE /dev/nvme1n1",
		},
		{
			name:      "wrong disk",
			input:     "ERASE /dev/nvme0n1\n",
			expected:  "ERASE /dev/nvme1n1",
			wantError: true,
		},
		{
			name:      "case mismatch",
			input:     "erase /dev/nvme1n1\n",
			expected:  "ERASE /dev/nvme1n1",
			wantError: true,
		},
		{
			name:      "leading whitespace",
			input:     " ERASE /dev/nvme1n1\n",
			expected:  "ERASE /dev/nvme1n1",
			wantError: true,
		},
		{
			name:      "trailing whitespace",
			input:     "ERASE /dev/nvme1n1 \n",
			expected:  "ERASE /dev/nvme1n1",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			ui := UI{
				Reader: bufio.NewReader(strings.NewReader(tt.input)),
				Out:    &out,
				GTK:    false,
			}

			err := ui.Exact(
				context.Background(),
				"Type the destructive confirmation",
				tt.expected,
			)

			if tt.wantError && err == nil {
				t.Fatal("invalid confirmation was accepted")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("exact confirmation rejected: %v", err)
			}
		})
	}
}

func TestExactConfirmationRequiresExpectedValue(t *testing.T) {
	var out bytes.Buffer
	ui := UI{
		Reader: bufio.NewReader(strings.NewReader("\n")),
		Out:    &out,
	}

	if err := ui.Exact(context.Background(), "Confirm", ""); err == nil {
		t.Fatal("empty expected confirmation was accepted")
	}
}

func TestGTKConfirmationWidthBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    int
	}{
		{
			name:    "short yes no remains compact",
			message: "Continue?",
			want:    420,
		},
		{
			name:    "compact upper boundary",
			message: strings.Repeat("a", 90),
			want:    420,
		},
		{
			name:    "normal by total length",
			message: strings.Repeat("a", 91),
			want:    520,
		},
		{
			name:    "normal upper boundary",
			message: strings.Repeat("a", 180) + "\n\n" + strings.Repeat("b", 60),
			want:    520,
		},
		{
			name:    "long by paragraph length",
			message: strings.Repeat("a", 181),
			want:    680,
		},
		{
			name:    "long by total length",
			message: strings.Repeat("a", 120) + "\n\n" + strings.Repeat("b", 121),
			want:    680,
		},
		{
			name:    "long upper boundary",
			message: strings.Repeat("a", 300) + "\n\n" + strings.Repeat("b", 300),
			want:    680,
		},
		{
			name:    "very long by total length",
			message: strings.Repeat("a", 300) + "\n\n" + strings.Repeat("b", 301),
			want:    760,
		},
		{
			name:    "very long by paragraph length",
			message: strings.Repeat("a", 401),
			want:    760,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gtkConfirmationWidth(tt.message); got != tt.want {
				t.Fatalf(
					"gtkConfirmationWidth() = %d, want %d",
					got,
					tt.want,
				)
			}
		})
	}
}

func TestGTKConfirmationWidthCountsRunesNotBytes(t *testing.T) {
	message := strings.Repeat("ä", 90)

	if got := gtkConfirmationWidth(message); got != 420 {
		t.Fatalf(
			"90 Unicode runes produced width %d, want compact 420",
			got,
		)
	}

	if len(message) <= 90 {
		t.Fatal("test fixture must contain more bytes than runes")
	}
}

func TestGTKConfirmationWidthLongRecoveryCompatibilityWarning(t *testing.T) {
	message := `The existing installation cannot use the requested trusted recovery layout with its current filesystem configuration.

GjallarOS recovery requires compatible Btrfs state so the recovery environment can repair or reconstruct the installed system safely. Continuing without that compatibility changes which recovery guarantees are available.

Do you want to continue without the incompatible recovery configuration?`

	if got := gtkConfirmationWidth(message); got != 680 {
		t.Fatalf(
			"long recovery compatibility warning width = %d, want 680",
			got,
		)
	}
}

func TestGTKConfirmationWidthVeryLongDeviceCompatibilityWarning(t *testing.T) {
	message := `The detected device does not currently have a validated firmware ownership-transfer procedure for this security operation.

GjallarOS uses the resolved ODDC device profile to determine whether firmware behavior has been validated for the exact device family. The device policy may describe supported firmware actions, but it cannot execute commands or bypass the trusted installer boundary.

Continuing with an unsupported firmware procedure could leave the machine unable to boot, replace firmware-owned Secure Boot material, or produce a configuration that cannot be recovered automatically.

This installation can continue only through a supported compatibility path. Review the detected device and security settings before proceeding.`

	if got := gtkConfirmationWidth(message); got != 760 {
		t.Fatalf(
			"very long device compatibility warning width = %d, want 760",
			got,
		)
	}
}

func TestNewWithInjectedIODisablesGTK(t *testing.T) {
	var out bytes.Buffer

	ui := New(strings.NewReader("yes\n"), &out)

	if ui.GTK {
		t.Fatal("injected prompt I/O unexpectedly enabled GTK")
	}
}

func TestTerminalSecureBootHandoffPlacesFirmwareLockInFinalVisit(t *testing.T) {
	lock := []string{"Set a firmware supervisor password."}
	var out strings.Builder
	// understood=y, show again=n
	u := UI{Reader: bufio.NewReader(strings.NewReader("y\nn\n")), Out: &out}
	if err := u.SecureBootFirmwareHandoff(context.Background(), "Test UEFI", []string{"Remove only PK."}, lock); err != nil {
		t.Fatal(err)
	}
	shown := out.String()
	want := "1. Enable Secure Boot.\n2. Set a firmware supervisor password.\n3. Save the firmware configuration.\n4. Boot GjallarOS.\n"
	if !strings.Contains(shown, want) {
		t.Fatalf("enrollment handoff does not announce the final firmware visit with the lock step:\n%s", shown)
	}

	out.Reset()
	u = UI{Reader: bufio.NewReader(strings.NewReader("y\nn\n")), Out: &out}
	if err := u.SecureBootEnableHandoff(context.Background(), "Test UEFI", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "supervisor") {
		t.Fatalf("enable handoff asks for a firmware password the user declined:\n%s", out.String())
	}
}

func TestShowSecureBootRecoveryKeepsSecretOutOfLogs(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	// Without /dev/tty (as in most test sandboxes) the call fails; with
	// one the secret goes there. The log never receives it.
	_ = UI{Out: log}.ShowSecureBootRecovery(context.Background(), "/archive", "passphrase-secret")
	data, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "passphrase-secret") {
		t.Fatalf("recovery passphrase written to non-terminal output: %q", data)
	}
}

func TestTerminalPromptStopsOnCancel(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	var out strings.Builder
	u := UI{Reader: bufio.NewReader(reader), Out: &out}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := u.Value(ctx, "Model", "default")
		errs <- err
	}()
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("Value after cancel = %v", err)
	}
	// The next question must not take a line typed after Ctrl-C.
	if _, err := u.Confirm(ctx, "Continue?", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("Confirm after cancel = %v", err)
	}
}

func TestExampleFillsNothingIn(t *testing.T) {
	var out strings.Builder
	u := UI{Reader: bufio.NewReader(strings.NewReader("\n  \nsam\n\n")), Out: &out}

	value, err := u.Example(context.Background(), "Username", "alex", true)
	if err != nil || value != "sam" {
		t.Fatalf("required: got %q, %v, want sam", value, err)
	}
	if got := strings.Count(out.String(), "Username is required."); got != 2 {
		t.Fatalf("asked again %d times, want 2; output %q", got, out.String())
	}

	value, err = u.Example(context.Background(), "GitHub username", "alex-example", false)
	if err != nil || value != "" {
		t.Fatalf("optional: got %q, %v, want empty", value, err)
	}

	text := out.String()
	for _, want := range []string{"Username (e.g. alex): ", "GitHub username (optional, e.g. alex-example): "} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "[") {
		t.Fatalf("output %q shows a filled-in default", text)
	}
}

func TestExampleStopsAtEndOfInput(t *testing.T) {
	u := UI{Reader: bufio.NewReader(strings.NewReader("\n")), Out: io.Discard}
	if _, err := u.Example(context.Background(), "Username", "alex", true); err == nil {
		t.Fatal("Example kept asking after the input ended")
	}
}

func TestTerminalChecklist(t *testing.T) {
	items := []Item{{Value: "a", Name: "A", Description: "first", On: true}, {Value: "b", Name: "B", Description: "second"}, {Value: "c", Name: "C", Description: "third"}}
	for input, want := range map[string]string{
		"\n":           "a",
		"none\n":       "",
		"3, 2\n":       "b,c",
		"1,1,3\n":      "a,c",
		"4\n0\nx\n2\n": "b",
		"1,,2\n3\n":    "c",
		"none,1\n1\n":  "a",
	} {
		var out strings.Builder
		u := UI{Reader: bufio.NewReader(strings.NewReader(input)), Out: &out}
		got, err := u.Checklist(context.Background(), "Pick", items)
		if err != nil || strings.Join(got, ",") != want {
			t.Errorf("%q: got %v, %v; want %q", input, got, err, want)
		}
	}
}
