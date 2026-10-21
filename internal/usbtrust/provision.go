package usbtrust

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProvisionTPM explicitly creates a non-exportable P-256 signing primary at
// an owner persistent handle. It never evicts an existing object. A handle
// that already holds the same primary (left by an earlier install on this TPM)
// is adopted: primaries derive from the owner seed and template, so a match is
// the very key this call would create.
func ProvisionTPM(ctx context.Context, dir, handle string) error {
	signer, err := NewTPMSigner(handle)
	if err != nil {
		return err
	}
	return provisionTPM(ctx, dir, handle, signer, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	})
}

type provisionCommand func(context.Context, string, ...string) ([]byte, error)

func provisionTPM(ctx context.Context, dir, handle string, signer Signer, command provisionCommand) (result error) {
	if !strings.HasPrefix(strings.ToLower(handle), "0x810") {
		return fmt.Errorf("USB trust requires an owner persistent handle in 0x81000000..0x810fffff")
	}
	if present, err := SignedStatePresent(dir); err != nil {
		return err
	} else if present {
		return fmt.Errorf("refusing to provision a new key over existing USB trust state")
	}
	output, err := command(ctx, "tpm2_getcap", "handles-persistent")
	if err != nil {
		return fmt.Errorf("inspect TPM handles: %w: %s", err, output)
	}
	occupied := strings.Contains(strings.ToLower(string(output)), strings.ToLower(handle))
	tmp, err := os.MkdirTemp("", "gjallar-usbtrust-provision-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	object := filepath.Join(tmp, "signing.ctx")
	run := func(name string, args ...string) error {
		output, err := command(ctx, name, args...)
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, output)
		}
		return nil
	}
	if err := run("tpm2_createprimary", "-Q", "-C", "o", "-G", "ecc256:ecdsa-sha256", "-g", "sha256", "-a", "fixedtpm|fixedparent|sensitivedataorigin|userwithauth|sign", "-c", object); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = command(cleanup, "tpm2_flushcontext", object)
	}()
	if occupied {
		same, err := sameTPMObject(ctx, command, handle, object)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("TPM handle %s is occupied by a different object; choose an unused handle", handle)
		}
		if err := ValidateSigner(ctx, signer); err != nil {
			return fmt.Errorf("existing key at %s failed the self-test: %w", handle, err)
		}
		return pinProvisionedKey(ctx, dir, signer)
	}
	if err := run("tpm2_evictcontrol", "-Q", "-C", "o", "-c", object, handle); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := command(cleanup, "tpm2_evictcontrol", "-Q", "-C", "o", "-c", handle)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("rollback USB trust key at %s failed; handle may remain occupied: %w: %s", handle, err, output))
		}
	}()
	if err := ValidateSigner(ctx, signer); err != nil {
		return fmt.Errorf("key provisioned at %s but self-test failed: %w", handle, err)
	}
	if err := pinProvisionedKey(ctx, dir, signer); err != nil {
		return err
	}
	committed = true
	return nil
}

// pinProvisionedKey records the key just created or adopted. It replaces a
// pin left without signed state: provisioning is where the trust starts.
func pinProvisionedKey(ctx context.Context, dir string, signer Signer) error {
	tpm, ok := signer.(*TPMSigner)
	if !ok {
		return nil
	}
	if err := PinKey(ctx, dir, tpm); err != nil {
		return fmt.Errorf("pin USB trust key: %w", err)
	}
	return nil
}

// sameTPMObject compares TPM object names. A name hashes the whole public
// area (key, template and attributes), so equal names mean the same key.
func sameTPMObject(ctx context.Context, command provisionCommand, a, b string) (bool, error) {
	name := func(object string) (string, error) {
		output, err := command(ctx, "tpm2_readpublic", "-c", object)
		if err != nil {
			return "", fmt.Errorf("tpm2_readpublic %s: %w: %s", object, err, output)
		}
		for _, line := range strings.Split(string(output), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "name:"); ok {
				if value = strings.TrimSpace(value); value != "" {
					return value, nil
				}
			}
		}
		return "", fmt.Errorf("tpm2_readpublic %s printed no object name", object)
	}
	first, err := name(a)
	if err != nil {
		return false, err
	}
	second, err := name(b)
	if err != nil {
		return false, err
	}
	return first == second, nil
}
