package stick

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The layout lsblk -J -l -p prints on a test machine with one internal disk
// and the installer stick on USB.
const lsblkRoots = `{"blockdevices":[
 {"path":"/dev/sda","type":"disk","fstype":"iso9660","partlabel":null,"pkname":null,"tran":"usb","rm":true},
 {"path":"/dev/sda1","type":"part","fstype":"btrfs","partlabel":"GJALLAROS","pkname":"/dev/sda","tran":null,"rm":true},
 {"path":"/dev/vda","type":"disk","fstype":null,"partlabel":null,"pkname":null,"tran":"virtio","rm":false},
 {"path":"/dev/vda1","type":"part","fstype":"vfat","partlabel":"ESP","pkname":"/dev/vda","tran":null,"rm":false},
 {"path":"/dev/vda2","type":"part","fstype":"crypto_LUKS","partlabel":"GJALLAROS","pkname":"/dev/vda","tran":null,"rm":false},
 {"path":"/dev/nvme0n1","type":"disk","fstype":null,"partlabel":null,"pkname":null,"tran":"nvme","rm":"0"},
 {"path":"/dev/nvme0n1p2","type":"part","fstype":"btrfs","partlabel":"GJALLAROS","pkname":"/dev/nvme0n1","tran":null,"rm":"0"},
 {"path":"/dev/nvme0n1p3","type":"part","fstype":"ext4","partlabel":"GJALLAROS","pkname":"/dev/nvme0n1","tran":null,"rm":"0"},
 {"path":"/dev/sdb","type":"disk","fstype":null,"partlabel":null,"pkname":null,"tran":"sata","rm":true},
 {"path":"/dev/sdb1","type":"part","fstype":"btrfs","partlabel":"GJALLAROS","pkname":"/dev/sdb","tran":null,"rm":true}
]}`

func TestInstalledRoots(t *testing.T) {
	roots, err := installedRoots([]byte(lsblkRoots))
	if err != nil {
		t.Fatal(err)
	}
	want := []rootPart{{Path: "/dev/vda2", Encrypted: true}, {Path: "/dev/nvme0n1p2"}}
	if len(roots) != len(want) {
		t.Fatalf("roots = %+v, want %+v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Errorf("root %d = %+v, want %+v", i, roots[i], want[i])
		}
	}
	if _, err := installedRoots([]byte("{")); err == nil {
		t.Error("broken lsblk output accepted")
	}
}

func TestRemovableUnknownCounts(t *testing.T) {
	for _, v := range []any{true, "1", float64(1), nil, "yes"} {
		if !removable(v) {
			t.Errorf("removable(%v) = false", v)
		}
	}
	for _, v := range []any{false, "0", float64(0)} {
		if removable(v) {
			t.Errorf("removable(%v) = true", v)
		}
	}
}

func testCertPEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gjallarOS Secure Boot key 2026-11-17"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<32, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return der, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// keyRoot makes the db folder below a temporary root and returns the root
// open and the db folder's path.
func keyRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	db := filepath.Join(root, filepath.Dir(keyFile))
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, db
}

func readKeyAt(t *testing.T, root string) ([]byte, error) {
	t.Helper()
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return readKey(f)
}

func TestReadKey(t *testing.T) {
	der, p := testCertPEM(t)
	root, db := keyRoot(t)
	if err := os.WriteFile(filepath.Join(db, "db.pem"), append(p, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readKeyAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(der) {
		t.Error("readKey returned other bytes than the certificate")
	}
}

// A link planted for the key file or for a folder above it is refused, also
// when it points at a good certificate.
func TestReadKeyRefusesLinks(t *testing.T) {
	_, p := testCertPEM(t)
	elsewhere := t.TempDir()
	good := filepath.Join(elsewhere, "db.pem")
	if err := os.WriteFile(good, p, 0o600); err != nil {
		t.Fatal(err)
	}

	root, db := keyRoot(t)
	if err := os.Symlink(good, filepath.Join(db, "db.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyAt(t, root); err == nil {
		t.Error("a linked key file was read")
	}

	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib/sbctl/keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "var/lib/sbctl/keys/db")); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyAt(t, root); err == nil {
		t.Error("a linked db folder was followed")
	}

	root = t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "var")); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyAt(t, root); err == nil {
		t.Error("a linked var folder was followed")
	}
}

// A FIFO would block the read forever; it is refused without waiting.
func TestReadKeyRefusesFIFO(t *testing.T) {
	root, db := keyRoot(t)
	if err := syscall.Mkfifo(filepath.Join(db, "db.pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readKeyAt(t, root); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("FIFO: err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readKey blocked on a FIFO")
	}
}

func TestReadKeyRefusesBadContent(t *testing.T) {
	_, p := testCertPEM(t)
	_, p2 := testCertPEM(t)
	for name, content := range map[string][]byte{
		"oversize":      append(p, make([]byte, maxKeyFile)...),
		"not PEM":       []byte("not a certificate\n"),
		"two certs":     append(append([]byte{}, p...), p2...),
		"private key":   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}),
		"trailing text": append(append([]byte{}, p...), []byte("more\n")...),
		"empty":         nil,
	} {
		root, db := keyRoot(t)
		if err := os.WriteFile(filepath.Join(db, "db.pem"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readKeyAt(t, root); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadKeyRefusesFolder(t *testing.T) {
	root, db := keyRoot(t)
	if err := os.Mkdir(filepath.Join(db, "db.pem"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeyAt(t, root); err == nil {
		t.Error("a folder named db.pem was accepted")
	}
}

// The menu shows Queue the key again only when shim started the stick.
func TestNumberedRequeueOnlyWithShim(t *testing.T) {
	for _, shim := range []bool{false, true} {
		_, keys := numbered(false, shim)
		found := false
		for _, a := range keys {
			if a == actRequeue {
				found = true
			}
		}
		if found != shim {
			t.Errorf("shim %v: Queue the key again shown = %v", shim, found)
		}
		if keys["3"] != map[bool]action{false: actShell, true: actRequeue}[shim] {
			t.Errorf("shim %v: entry 3 = %v", shim, keys["3"])
		}
	}
}

func TestCryptsetupFailure(t *testing.T) {
	exit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	raw := []byte("No key available with this passphrase.\n")
	if got := cryptsetupFailure(exit("2"), raw).Error(); got != "that password or recovery key did not open the disk" {
		t.Errorf("exit 2: %q", got)
	}
	busy := []byte("Device \x1b[31mroot\x1b[0m is busy.\n\nsecond line\n")
	got := cryptsetupFailure(exit("5"), busy).Error()
	if strings.ContainsRune(got, '\x1b') || got != "cryptsetup could not open the disk: Device [31mroot[0m is busy.; second line" {
		t.Errorf("exit 5: %q", got)
	}
	if got := cryptsetupFailure(exit("4"), nil).Error(); got != "cryptsetup could not open the disk: exit status 4" {
		t.Errorf("exit 4 without text: %q", got)
	}
	long := cryptsetupFailure(exit("1"), []byte(strings.Repeat("x", 500))).Error()
	if n := len([]rune(long)); n != len([]rune("cryptsetup could not open the disk: "))+201 {
		t.Errorf("long text not capped: %d runes", n)
	}
	var c capWriter
	c.max = 4
	if n, err := c.Write([]byte("abcdef")); n != 6 || err != nil || c.buf.String() != "abcd" {
		t.Errorf("capWriter: %d %v %q", n, err, c.buf.String())
	}
}
