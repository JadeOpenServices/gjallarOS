// Package policy is the authoritative, model-independent agent tool policy.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Risk int

const (
	ReadOnly Risk = iota + 1
	WorkspaceMutation
	Validation
	Privileged
	Sensitive
)

type Profile struct {
	Mode              string `json:"mode"`
	WorkspaceWrites   bool   `json:"workspaceWrites"`
	LocalReads        bool   `json:"localReads"`
	LocalWrites       bool   `json:"localWrites"`
	LocalShell        bool   `json:"localShell"`
	NixBuilds         bool   `json:"nixBuilds"`
	ServiceInspection bool   `json:"serviceInspection"`
	ServiceManagement bool   `json:"serviceManagement"`
	SystemDeployment  bool   `json:"systemDeployment"`
	ExternalWrites    bool   `json:"externalWrites"`
	SecretAccess      bool   `json:"secretAccess"`
}

type Decision struct {
	Capability string
	Risk       Risk
	Allowed    bool
	Approval   bool
	Reason     string
}

var secretParts = []string{"/.ssh/", "/.gnupg/", "/.password-store/", "/.thunderbird/", "/keyrings/", "/credentials/", "/secrets/"}

func DefaultProfile(mode string) (Profile, error) {
	switch mode {
	case "workspace", "":
		return Profile{Mode: "workspace", WorkspaceWrites: true, NixBuilds: true, ServiceInspection: true}, nil
	case "owner-conservative":
		return Profile{Mode: mode, WorkspaceWrites: true, LocalReads: true, LocalWrites: true, LocalShell: true, NixBuilds: true, ServiceInspection: true, ServiceManagement: true}, nil
	case "owner-full-local":
		return Profile{Mode: mode, WorkspaceWrites: true, LocalReads: true, LocalWrites: true, LocalShell: true, NixBuilds: true, ServiceInspection: true, ServiceManagement: true, SystemDeployment: true}, nil
	default:
		return Profile{}, fmt.Errorf("unknown agent policy mode %q", mode)
	}
}

func IsSecret(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path)) + "/"
	for _, part := range secretParts {
		if strings.Contains(clean, part) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.Contains(base, "private_key") || strings.HasSuffix(base, ".pem")
}

func Within(workspace, path string) bool {
	w, err1 := filepath.Abs(workspace)
	p, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(w, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Decide classifies narrow tools. Caller-supplied labels never affect policy.
func Decide(p Profile, workspace, tool string, args []string) Decision {
	joined := strings.ToLower(strings.Join(args, " "))
	for _, forbidden := range []string{"sudo ", "doas ", "pkexec ", "git push", "git clean", "git reset --hard", "curl ", "wget ", "scp ", "sftp ", "ssh ", "nc "} {
		if strings.Contains(joined, forbidden) {
			return Decision{tool, Sensitive, false, true, "external, destructive, or arbitrary-network action"}
		}
	}
	for _, arg := range args {
		if filepath.IsAbs(arg) && IsSecret(arg) && !p.SecretAccess {
			return Decision{tool, Sensitive, false, false, "secret path is protected"}
		}
	}
	switch tool {
	case "repo-read", "repo-search", "git-inspect":
		return Decision{tool, ReadOnly, true, false, "bounded read"}
	case "repo-format", "repo-test":
		return Decision{tool, Validation, p.WorkspaceWrites, false, "workspace validation"}
	case "nix-eval", "nix-check", "nix-build", "nixos-dry-build":
		return Decision{tool, Validation, p.NixBuilds, false, "bounded Nix validation"}
	case "system-inspect":
		return Decision{tool, ReadOnly, p.ServiceInspection, false, "read-only system diagnostics"}
	case "local-command":
		return Decision{tool, Validation, p.LocalShell, false, "owner-mode local command"}
	case "service-manage":
		return Decision{tool, Privileged, p.ServiceManagement, !p.ServiceManagement, "live service change"}
	case "nixos-deploy":
		return Decision{tool, Privileged, p.SystemDeployment, !p.SystemDeployment, "live NixOS deployment"}
	default:
		return Decision{tool, Sensitive, false, false, "unknown capability"}
	}
}

type Grant struct {
	ActionHash string    `json:"actionHash"`
	Expires    time.Time `json:"expires"`
}

func ActionHash(workspace, tool string, args []string) string {
	h := sha256.Sum256([]byte(filepath.Clean(workspace) + "\x00" + tool + "\x00" + strings.Join(args, "\x00")))
	return hex.EncodeToString(h[:])
}

func WriteGrant(path, workspace, tool string, args []string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, _ := json.Marshal(Grant{ActionHash: ActionHash(workspace, tool, args), Expires: now.Add(5 * time.Minute)})
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// ConsumeGrant is exact-action and one-use. It removes state before execution.
func ConsumeGrant(path, workspace, tool string, args []string, now time.Time) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("explicit approval required")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("consume approval: %w", err)
	}
	var g Grant
	if json.Unmarshal(b, &g) != nil || now.After(g.Expires) || g.ActionHash != ActionHash(workspace, tool, args) {
		return errors.New("approval is expired or does not match this exact action")
	}
	return nil
}
