package secrets

import (
	"bufio"
	"context"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/termout"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

type Status struct {
	SecretsExist, KeyExists, SOPSAvailable, Decrypts bool
	KeyPath                                          string
}

func EnsureAgeKey(ctx context.Context, username string) (keyPath, recipient string, err error) {
	account, err := user.Lookup(username)
	if err != nil {
		return "", "", err
	}
	keyPath = filepath.Join(account.HomeDir, ".config", "sops", "age", "keys.txt")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		created := missingDirs(account.HomeDir, filepath.Dir(keyPath))
		if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
			return "", "", err
		}
		// Run as root (sudo installer), the key and the directories made
		// for it must still belong to the user whose sops reads them.
		defer func() {
			if err == nil {
				err = giveToAccount(account, append(created, keyPath)...)
			}
		}()
		tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".age-key-*")
		if err != nil {
			return "", "", err
		}
		tmpPath := tmp.Name()
		tmp.Close()
		os.Remove(tmpPath)
		defer os.Remove(tmpPath)
		cmd := exec.CommandContext(ctx, "age-keygen", "-o", tmpPath)
		if err := termout.Attach(cmd); err != nil {
			return "", "", fmt.Errorf("generate age key: %w", err)
		}
		if err := os.Chmod(tmpPath, 0600); err != nil {
			return "", "", err
		}
		if err := os.Rename(tmpPath, keyPath); err != nil {
			return "", "", err
		}
	}
	f, err := os.Open(keyPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "public key:") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				recipient = fields[len(fields)-1]
				break
			}
		}
	}
	if !strings.HasPrefix(recipient, "age1") {
		return "", "", fmt.Errorf("could not read public recipient from %s", keyPath)
	}
	return keyPath, recipient, nil
}

// missingDirs lists dir and its parents below home that do not exist yet,
// outermost first.
func missingDirs(home, dir string) []string {
	var missing []string
	for d := dir; d != home && strings.HasPrefix(d, home+string(filepath.Separator)); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append([]string{d}, missing...)
	}
	return missing
}

var (
	secretsEUID   = os.Geteuid
	secretsLchown = os.Lchown
)

func giveToAccount(account *user.User, paths ...string) error {
	if secretsEUID() != 0 {
		return nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return fmt.Errorf("uid of %s: %w", account.Username, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return fmt.Errorf("gid of %s: %w", account.Username, err)
	}
	for _, path := range paths {
		if err := secretsLchown(path, uid, gid); err != nil {
			return fmt.Errorf("give %s to %s: %w", path, account.Username, err)
		}
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sops-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func Check(ctx context.Context, repo, username string) (Status, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return Status{}, fmt.Errorf("look up user %q: %w", username, err)
	}
	key := filepath.Join(account.HomeDir, ".config", "sops", "age", "keys.txt")
	secret := filepath.Join(repo, "secrets", "default.yaml")
	status := Status{KeyPath: key}
	if info, err := os.Stat(secret); err == nil && !info.IsDir() {
		status.SecretsExist = true
	} else if err != nil && !os.IsNotExist(err) {
		return status, err
	}
	if info, err := os.Stat(key); err == nil && !info.IsDir() {
		status.KeyExists = true
	}
	if _, err := exec.LookPath("sops"); err == nil {
		status.SOPSAvailable = true
	}
	if status.SecretsExist && status.KeyExists && status.SOPSAvailable {
		cmd := exec.CommandContext(ctx, "sops", "--decrypt", secret)
		cmd.Stdout = nil
		cmd.Stderr = nil
		status.Decrypts = cmd.Run() == nil
	}
	return status, nil
}
