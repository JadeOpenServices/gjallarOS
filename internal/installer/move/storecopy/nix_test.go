package storecopy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The whole store copy with the nix on PATH, as this user and without a
// daemon: a path goes into an old store, the export's own nix arguments
// copy it into an empty cache, Settle takes the cache, and a fresh store
// imports it with the signature rule of SubstituteArgs. The store path, its
// content and the signing key are made up here (SYNTHETIC); nothing is
// fetched (L62).

type nixRun struct {
	t   *testing.T
	bin string
	env []string
}

func newNixRun(t *testing.T) nixRun {
	t.Helper()
	bin, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not on PATH")
	}
	home := t.TempDir()
	return nixRun{t: t, bin: bin, env: []string{
		"PATH=" + filepath.Dir(bin),
		"HOME=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"NIX_CONFIG=substituters =\nexperimental-features = " + features,
	}}
}

// run runs a program of nix's bin folder with stdin and returns what it
// printed: its output when it must work, its errors when it must fail.
func (n nixRun) run(fail bool, prog, stdin string, args ...string) string {
	n.t.Helper()
	cmd := exec.Command(filepath.Join(filepath.Dir(n.bin), prog), args...)
	cmd.Env = n.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); (err != nil) != fail {
		n.t.Fatalf("%s %q: %v\n%s", prog, args, err, errs.String())
	}
	if fail {
		return errs.String()
	}
	return out.String()
}

// testPath is an input-addressed store path: nix cannot tell its content
// from its name, so only a signature makes it trusted.
const testPath = "/nix/store/" + pathHash + "-gjallar-move-test"

// handCache writes a cache holding testPath with the given file inside.
func (n nixRun) handCache(dir, content string) {
	n.t.Helper()
	src := filepath.Join(n.t.TempDir(), "src")
	put(n.t, src, content)
	nar := filepath.Join(n.t.TempDir(), "nar")
	data := n.run(false, "nix-store", "", "--dump", src)
	put(n.t, nar, data)
	hash := strings.TrimSpace(n.run(false, "nix-hash", "", "--type", "sha256", "--flat", "--base32", nar))
	put(n.t, filepath.Join(dir, cacheInfo), cacheInfoText)
	put(n.t, filepath.Join(dir, narDir, hash+".nar"), data)
	size := strconv.Itoa(len(data))
	put(n.t, filepath.Join(dir, pathHash+".narinfo"), "StorePath: "+testPath+"\nURL: nar/"+hash+
		".nar\nCompression: none\nFileHash: sha256:"+hash+"\nFileSize: "+size+
		"\nNarHash: sha256:"+hash+"\nNarSize: "+size+"\nReferences: \n")
}

// exportArgs is ExportArgs' nix command with the unit's folders swapped for
// test folders.
func exportArgs(t *testing.T, old, cache string) []string {
	t.Helper()
	args, err := ExportArgs("/o", "/c", testPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := slices.Index(args, "--")
	nix := slices.Clone(args[cmd+2:])
	for i, a := range nix {
		a = strings.ReplaceAll(a, "file://"+cacheDir, "file://"+cache)
		nix[i] = strings.ReplaceAll(a, "="+oldNix+"/", "="+old+"/")
	}
	return nix
}

// importArgs is a copy from the cache into a fresh store under the signature
// rule SubstituteArgs gives nixos-install.
func importArgs(t *testing.T, cache, root string, keys ...string) []string {
	t.Helper()
	sub, err := SubstituteArgs(cache, keys...)
	if err != nil {
		t.Fatal(err)
	}
	var opts []string
	for i := 0; i+2 < len(sub); i += 3 {
		if sub[i+1] != "extra-substituters" {
			opts = append(opts, sub[i:i+3]...)
		}
	}
	return append(opts, "copy", "--from", "file://"+cache, "--to", "local?root="+root, testPath)
}

func TestNixStoreCopy(t *testing.T) {
	n := newNixRun(t)
	tmp := t.TempDir()
	sk := filepath.Join(tmp, "sk")
	secret := n.run(false, "nix", "", "key", "generate-secret", "--key-name", "gjallaros-test-1")
	put(t, sk, secret)
	pk := strings.TrimSpace(n.run(false, "nix", secret, "key", "convert-secret-to-public"))

	// The old disk's store holds the path.
	hand := filepath.Join(tmp, "hand")
	n.handCache(hand, "SYNTHETIC store content\n")
	old := filepath.Join(tmp, "old")
	n.run(false, "nix", "", "copy", "--no-check-sigs", "--from", "file://"+hand, "--to", "local?root="+old, testPath)

	// The export, then Settle.
	cache := filepath.Join(tmp, "cache")
	if err := os.Mkdir(cache, 0o777); err != nil {
		t.Fatal(err)
	}
	n.run(false, "nix", "", exportArgs(t, filepath.Join(old, "nix"), cache)...)
	settleReal := func() Stats {
		t.Helper()
		s, err := settle(t, cache, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := settleReal(); s.Paths != 1 || s.Bytes <= 0 {
		t.Fatalf("settled %+v", s)
	}

	// Unsigned: refused.
	unsigned := n.run(true, "nix", "", importArgs(t, cache, filepath.Join(tmp, "unsigned"), NixOSKey, pk)...)
	if !strings.Contains(unsigned, "lacks a signature by a trusted key") {
		t.Errorf("unsigned path refused for another reason:\n%s", unsigned)
	}

	// Signed with a key the install does not trust: refused.
	n.run(false, "nix", "", "store", "sign", "--store", "file://"+cache+"?compression=none", "--key-file", sk, testPath)
	settleReal()
	wrong := n.run(true, "nix", "", importArgs(t, cache, filepath.Join(tmp, "wrongkey"), NixOSKey)...)
	if !strings.Contains(wrong, "lacks a signature by a trusted key") {
		t.Errorf("untrusted key refused for another reason:\n%s", wrong)
	}

	// Signed with a trusted key: taken, and the new store verifies.
	good := filepath.Join(tmp, "good")
	n.run(false, "nix", "", importArgs(t, cache, good, NixOSKey, pk)...)
	verify, err := VerifyArgs(good, testPath)
	if err != nil {
		t.Fatal(err)
	}
	n.run(false, "nix", "", verify...)

	// The NAR changed after signing, same size: Settle cannot tell, nix
	// refuses it on import.
	nars, err := os.ReadDir(filepath.Join(cache, narDir))
	if err != nil || len(nars) != 1 {
		t.Fatalf("nars: %v, %v", nars, err)
	}
	narPath := filepath.Join(cache, narDir, nars[0].Name())
	data, err := os.ReadFile(narPath)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(data), "SYNTHETIC")
	data[i] = 's'
	if err := os.WriteFile(narPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	settleReal()
	out := n.run(true, "nix", "", importArgs(t, cache, filepath.Join(tmp, "tampered"), NixOSKey, pk)...)
	if !strings.Contains(out, "hash mismatch") {
		t.Errorf("tampered NAR refused for another reason:\n%s", out)
	}
}
