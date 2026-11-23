package diskcrypto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

// PCRLock is the systemd-pcrlock binary, set at link time
// (pkgs/gjallarctl, pkgs/gjallar-installer, pkgs/gjallar-settingsd). It is
// a store path and is never looked up on PATH. The TPM device is root-only
// (crw------- root root), so the check needs root: gjallar-chip-check and
// the stick menu run as root, an installer started by a user asks sudo.
var PCRLock string

// PCRLockSupport is the answer of `systemd-pcrlock is-supported`.
type PCRLockSupport string

const (
	// PCRLockSupported: TPM2 and every command the startup policy needs.
	PCRLockSupported PCRLockSupport = "yes"
	// PCRLockNoTPM: no usable TPM2.
	PCRLockNoTPM PCRLockSupport = "no"
	// PCRLockPartial: some TPM2 parts are missing.
	PCRLockPartial PCRLockSupport = "partial"
	// PCRLockObsolete: TPM2 works, but the chip lacks the commands the
	// policy uses (PolicyAuthorizeNV; friend's T480s, 2026-11-22).
	PCRLockObsolete PCRLockSupport = "obsolete"
)

// pcrlockEUID is os.Geteuid; tests replace it.
var pcrlockEUID = os.Geteuid

// pcrlockArgv is the command line for the check. As a user, the binary
// would only see "Permission denied" on /dev/tpmrm0 and every machine would
// look like it had no usable chip, so it runs through sudo. The arguments
// are fixed; nothing the user passes reaches root.
func pcrlockArgv(bin string, euid int) []string {
	if euid == 0 {
		return []string{bin, "is-supported"}
	}
	return []string{"sudo", "--", bin, "is-supported"}
}

// pcrlockRun runs the check and keeps stdout only, so a sudo or TSS
// message on stderr never mixes into the answer; tests replace it.
var pcrlockRun = func(ctx context.Context, bin string) ([]byte, int, error) {
	argv := pcrlockArgv(bin, pcrlockEUID())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"LC_ALL=C", "SYSTEMD_LOG_LEVEL=err"}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.Bytes(), exit.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, err
	}
	return stdout.Bytes(), 0, nil
}

// CheckPCRLockSupport asks systemd-pcrlock whether this TPM can carry the
// measured-boot policy that chip unlock depends on. The policy needs
// TPM2_PolicyAuthorizeNV; a chip without it makes `make-policy` refuse
// during the install, after the disk and the boot entry were written
// (friend's T480s, .68, 2026-11-22). Any answer other than PCRLockSupported means chip
// unlock must stay off. An error means the answer is unknown; callers
// handle that like a missing command.
func CheckPCRLockSupport(ctx context.Context) (PCRLockSupport, error) {
	if PCRLock == "" {
		return "", errors.New("this build does not know where systemd-pcrlock is")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, code, err := pcrlockRun(ctx, PCRLock)
	if err != nil {
		return "", fmt.Errorf("systemd-pcrlock is-supported: %w", err)
	}
	answer := PCRLockSupport(strings.TrimSpace(string(out)))
	switch answer {
	case PCRLockSupported:
		if code != 0 {
			return "", fmt.Errorf("systemd-pcrlock is-supported said yes but exited %d", code)
		}
		return answer, nil
	case PCRLockNoTPM, PCRLockPartial, PCRLockObsolete:
		return answer, nil
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return "", fmt.Errorf("systemd-pcrlock is-supported gave an unknown answer %q (exit %d)", out, code)
}

// Describe is the answer in words for notices.
func (s PCRLockSupport) Describe() string {
	switch s {
	case PCRLockSupported:
		return "the security chip supports the startup policy"
	case PCRLockNoTPM:
		return "no usable TPM 2.0 security chip was found"
	case PCRLockPartial:
		return "the security chip lacks parts of TPM 2.0 that the startup policy needs"
	case PCRLockObsolete:
		return "the security chip lacks the PolicyAuthorizeNV command that the startup policy needs"
	}
	return "the security chip could not be checked"
}

// ChipCheckPath is where gjallar-chip-check writes its boot answer. The
// TPM is root-only, so the settings service and the user's gjallarctl read
// this instead of asking the chip.
const ChipCheckPath = "/run/gjallar/chip-check.json"

// ReadChipCheck returns the reason in root's boot check: empty when chip
// unlock can be switched on. A file root did not write, a link, or a
// missing answer keeps chip unlock off.
func ReadChipCheck(path string) string {
	var fact struct {
		ChipReason *string `json:"chipReason"`
	}
	data, _, ok := nettrust.ReadRootFile(path)
	if !ok || json.Unmarshal(data, &fact) != nil || fact.ChipReason == nil {
		return "the boot check of the security chip has no answer, so it is not known whether the chip can carry the startup policy"
	}
	return *fact.ChipReason
}
