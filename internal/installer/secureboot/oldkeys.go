package secureboot

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// R47 (L95): every fresh install makes a new signing key. An older key
// left in shim's MokList keeps trusting everything it ever signed, so the
// MokManager visit that enrolls the new key also deletes the old ones.

// ourKeyName starts the name of every key gjallarOS makes since R47; the
// install date follows it.
const ourKeyName = "gjallarOS Secure Boot key"

// sbctlDefaultDBName is the name sbctl gives a db key made without a
// description. gjallarOS keys had it before R47, but so does a key of any
// other system on this computer that uses sbctl, so the user decides.
const sbctlDefaultDBName = "Database Key"

// sbctlConfig is the configuration sbctl create-keys reads from stdin: the
// sbctl defaults, with names that say whose keys these are and when they
// were made. Root reads it from the pipe, never from a file the user
// session could change.
func sbctlConfig(day string) ([]byte, error) {
	type key struct {
		Privkey     string `json:"privkey"`
		Pubkey      string `json:"pubkey"`
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	k := func(dir, desc string) key {
		return key{
			Privkey:     "/var/lib/sbctl/keys/" + dir + "/" + dir + ".key",
			Pubkey:      "/var/lib/sbctl/keys/" + dir + "/" + dir + ".pem",
			Type:        "file",
			Description: desc + " " + day,
		}
	}
	return json.Marshal(struct {
		Landlock  bool           `json:"landlock"`
		Keydir    string         `json:"keydir"`
		GUID      string         `json:"guid"`
		FilesDB   string         `json:"files_db"`
		BundlesDB string         `json:"bundles_db"`
		Keys      map[string]key `json:"keys"`
	}{
		Landlock:  true,
		Keydir:    "/var/lib/sbctl/keys",
		GUID:      "/var/lib/sbctl/GUID",
		FilesDB:   "/var/lib/sbctl/files.json",
		BundlesDB: "/var/lib/sbctl/bundles.json",
		Keys: map[string]key{
			"pk":  k("PK", "gjallarOS platform key"),
			"kek": k("KEK", "gjallarOS key exchange key"),
			"db":  k("db", ourKeyName),
		},
	})
}

// oldKey is a key in MokListRT that an earlier gjallarOS install may have
// made.
type oldKey struct {
	der  []byte
	name string
	// ours is a key named by gjallarOS since R47; the rest carry
	// sbctl's default name.
	ours bool
}

// oldMokKeys lists the keys in MokListRT other than current that carry a
// gjallarOS name or sbctl's default db name.
func oldMokKeys(f firmwareFiles, current []byte) ([]oldKey, error) {
	raw, present, err := f.shimVar("MokListRT")
	if err != nil || !present {
		return nil, err
	}
	list := parseSignatureList(raw, true)
	if list.Malformed {
		return nil, errors.New("MokListRT does not parse")
	}
	var keys []oldKey
	for _, c := range list.Certs {
		if string(c.der) == string(current) {
			continue
		}
		cert, err := x509.ParseCertificate(c.der)
		if err != nil {
			continue
		}
		switch cn := cert.Subject.CommonName; {
		case strings.HasPrefix(cn, ourKeyName):
			keys = append(keys, oldKey{der: c.der, name: certName(cert), ours: true})
		case cn == sbctlDefaultDBName:
			keys = append(keys, oldKey{der: c.der, name: certName(cert)})
		}
	}
	return keys, nil
}

// mokDeletePath is where root keeps the n-th key mokutil --delete reads.
func mokDeletePath(n int) string {
	return fmt.Sprintf("/run/gjallar-mok-delete-%d.der", n)
}

// queueOldKeys asks MokManager to delete older gjallarOS keys: keys with a
// gjallarOS name always, keys with sbctl's default name when the user
// agrees. It reports whether mokutil wrote a new request; password is
// printed before mokutil asks for one.
func queueOldKeys(ctx context.Context, ui UI, out io.Writer, f firmwareFiles, current []byte, password string) (bool, error) {
	keys, err := oldMokKeys(f, current)
	if err != nil {
		return false, err
	}
	var queue [][]byte
	for _, k := range keys {
		queued, err := mokHolds(f, "MokDel", k.der)
		if err != nil {
			return false, err
		}
		if queued {
			fmt.Fprintf(out, "The key %q already waits for MokManager to delete it.\n", k.name)
			continue
		}
		if k.ours {
			fmt.Fprintf(out, "An older gjallarOS key, %q, is still trusted. MokManager deletes it at the next start.\n", k.name)
			queue = append(queue, k.der)
			continue
		}
		fmt.Fprintf(out, "Shim also trusts a key named %q, fingerprint\n  %s\n", k.name, sha1Fingerprint(k.der))
		fmt.Fprintln(out, "Earlier gjallarOS installs made keys with this name, and so does any other system on this computer that uses sbctl.")
		yes, err := ui.Confirm(ctx, "Delete this key? Keep it if another system on this computer still starts with it.", false)
		if err != nil {
			return false, err
		}
		if yes {
			queue = append(queue, k.der)
		}
	}
	if len(queue) == 0 {
		return false, nil
	}
	// One EFI_SIGNATURE_LIST per key.
	size := 0
	for _, der := range queue {
		size += 28 + 16 + len(der)
	}
	if err := efiSpaceFor(f.efivars, size); err != nil {
		return false, err
	}
	// Root mokutil reads the keys from root-owned files written from
	// memory, never from a path the user session can change (L54, L95).
	paths := make([]string, len(queue))
	for i, der := range queue {
		paths[i] = mokDeletePath(i)
		if err := writeRootFile(ctx, der, "0600", paths[i]); err != nil {
			return false, fmt.Errorf("stage an old key for mokutil: %w", err)
		}
	}
	defer func() {
		_ = run(context.WithoutCancel(ctx), "sudo", append([]string{"rm", "-f", "--"}, paths...)...)
	}()
	fmt.Fprintln(out, password)
	if err := run(ctx, "sudo", append([]string{privilegedTool("mokutil"), "--delete"}, paths...)...); err != nil {
		return false, fmt.Errorf("queue the old keys for deletion: %w", err)
	}
	for _, der := range queue {
		queued, err := mokHolds(f, "MokDel", der)
		if err != nil {
			return false, err
		}
		if !queued {
			return false, errors.New("mokutil returned, but the firmware holds no request to delete an old key")
		}
	}
	return true, nil
}

// clearForeignMokDel looks for delete requests that gjallarOS would not
// make. MokManager confirms all requests at once, so a stale one would go
// through with ours; the user may drop them all (mokutil --revoke-delete)
// and gjallarOS queues its own again.
func clearForeignMokDel(ctx context.Context, ui UI, out io.Writer, f firmwareFiles, c stickCopy, current []byte) error {
	raw, present, err := f.shimRequest("MokDel")
	if err != nil || !present {
		return err
	}
	lists, err := signatureLists(raw)
	if err != nil {
		return fmt.Errorf("MokDel: %w", err)
	}
	keys, err := oldMokKeys(f, current)
	if err != nil {
		return err
	}
	stick, _, err := f.stickHash(c)
	if err != nil {
		return err
	}
	var foreign []string
	for _, l := range lists {
		for _, cert := range l.Certs {
			if !slices.ContainsFunc(keys, func(k oldKey) bool { return string(k.der) == string(cert.der) }) {
				foreign = append(foreign, "the key "+entryName(cert.der))
			}
		}
		for _, sum := range l.FileSums {
			if sum != stick {
				foreign = append(foreign, "the file hash "+spacedHex(sum[:16]))
			}
		}
		for range l.Hashes - len(l.FileSums) {
			foreign = append(foreign, "a certificate hash")
		}
		for range l.Other {
			foreign = append(foreign, "an entry of an unknown type")
		}
	}
	if len(foreign) == 0 {
		return nil
	}
	fmt.Fprintln(out, "MokManager also holds requests to delete entries that gjallarOS did not ask for:")
	for _, s := range foreign {
		fmt.Fprintln(out, "  "+s)
	}
	fmt.Fprintln(out, "MokManager confirms all delete requests at once.")
	drop, err := ui.Confirm(ctx, "Drop these requests? gjallarOS then asks for its own again.", false)
	if err != nil {
		return err
	}
	if !drop {
		return errors.New("MokManager holds delete requests that gjallarOS did not make; finish them first or drop them with mokutil --revoke-delete")
	}
	if err := run(ctx, "sudo", privilegedTool("mokutil"), "--revoke-delete"); err != nil {
		return fmt.Errorf("drop the delete requests: %w", err)
	}
	if _, present, err := f.shimRequest("MokDel"); err != nil || present {
		if err == nil {
			err = errors.New("mokutil returned, but the delete requests are still there")
		}
		return err
	}
	return nil
}

// signatureLists splits a variable into its EFI_SIGNATURE_LISTs, in order.
// MokManager shows one "View key N" per list.
func signatureLists(raw []byte) ([]SignatureList, error) {
	var lists []SignatureList
	for len(raw) > 0 {
		if len(raw) < 28 {
			return nil, errors.New("does not parse")
		}
		size := int(binary.LittleEndian.Uint32(raw[16:20]))
		if size < 28 || size > len(raw) {
			return nil, errors.New("does not parse")
		}
		l := parseSignatureList(raw[:size], true)
		if l.Malformed {
			return nil, errors.New("does not parse")
		}
		lists = append(lists, l)
		raw = raw[size:]
	}
	return lists, nil
}

// readMokDel returns the queued delete requests in MokManager's order.
func readMokDel(f firmwareFiles) ([]SignatureList, error) {
	raw, present, err := f.shimRequest("MokDel")
	if err != nil || !present {
		return nil, err
	}
	lists, err := signatureLists(raw)
	if err != nil {
		return nil, fmt.Errorf("MokDel: %w", err)
	}
	return lists, nil
}

// entryName is a certificate's name for the screen.
func entryName(der []byte) string {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "(does not parse)"
	}
	return fmt.Sprintf("%q", certName(cert))
}
