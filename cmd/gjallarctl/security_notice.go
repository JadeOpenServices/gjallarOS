package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// securityFinding is one entry of `oddc security --json`. ODDC owns the
// facts and the wording; GjallarOS only decides when to show them.
type securityFinding struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Modified string `json:"modified"`
	Summary  string `json:"summary"`
}

func runSecurity(args []string, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "notice" {
		return runSecurityNotice(stderr)
	}
	if len(args) == 1 && args[0] == "oddc-notice" {
		return runODDCNotice(stderr)
	}
	fmt.Fprintln(stderr, "Usage: gjallarctl security notice|oddc-notice")
	return 2
}

// runSecurityNotice shows one calm notice when ODDC knows of a finding for
// this model that is new or changed since the user last saw one. It runs at
// login and after every rebuild, and stays quiet otherwise.
func runSecurityNotice(stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		findings, err := oddcFindings(ctx)
		if err != nil {
			// ODDC releases before 2 have no `oddc security`.
			fmt.Fprintf(stderr, "oddc security: %v\n", err)
			return 0
		}
		seen, err := readSeenFindings()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fresh := unseenFindings(findings, seen)
		if len(fresh) == 0 {
			return 0
		}
		summary, body := securityNoticeText(fresh)
		choice, err := showNotice(ctx, desktopNotice{
			App:     "GjallarOS",
			Icon:    "security-medium",
			Summary: summary,
			Body:    body,
			Actions: []noticeAction{{"details", "Details"}, {"ok", "OK"}},
		})
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if err := writeSeenFindings(findings); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if choice == "details" {
			// Plain `oddc security` is ODDC's text for users. --details is
			// for contributors and spells out how each weakness is used.
			script := `oddc security; printf '\nPress Enter to close. '; read -r _`
			// The title gives it a floating window, see user/wm/hyprland/rules.nix.
			if err := exec.Command("ghostty", "--title=Security notes for this device", "-e", "sh", "-c", script).Run(); err != nil {
				fmt.Fprintf(stderr, "ERROR: terminal: %v\n", err)
			}
		}
		// A rebuild while the notice was open may have brought more.
	}
}

func oddcFindings(ctx context.Context) ([]securityFinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "oddc", "security", "--json").Output()
	if err != nil {
		return nil, err
	}
	return parseSecurityFindings(out)
}

func parseSecurityFindings(out []byte) ([]securityFinding, error) {
	var report struct {
		Findings []securityFinding `json:"findings"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, err
	}
	return report.Findings, nil
}

// unseenFindings returns the findings that are new or whose modified date
// changed, most severe first.
func unseenFindings(findings []securityFinding, seen map[string]string) []securityFinding {
	var fresh []securityFinding
	for _, f := range findings {
		if f.ID == "" {
			continue
		}
		if m, ok := seen[f.ID]; !ok || m != f.Modified {
			fresh = append(fresh, f)
		}
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.SliceStable(fresh, func(i, j int) bool {
		ri, ok := rank[fresh[i].Severity]
		if !ok {
			ri = len(rank)
		}
		rj, ok := rank[fresh[j].Severity]
		if !ok {
			rj = len(rank)
		}
		return ri < rj
	})
	return fresh
}

// securityNoticeText words the notice. Each ODDC summary already says what
// to be careful with, so one finding shows its summary, and several show
// their names with the full text one click away.
func securityNoticeText(fresh []securityFinding) (summary, body string) {
	if len(fresh) == 1 {
		text := strings.TrimSpace(fresh[0].Summary)
		if text == "" {
			text = fresh[0].Name
		}
		return "Security note for this device", html.EscapeString(text)
	}
	var b strings.Builder
	for _, f := range fresh {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = f.ID
		}
		b.WriteString("• ")
		b.WriteString(html.EscapeString(name))
		b.WriteString("\n")
	}
	b.WriteString("\nDetails says what to be careful with.")
	return fmt.Sprintf("%d security notes for this device", len(fresh)), b.String()
}

func seenFindingsPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "gjallar", "security-findings-seen.json")
}

func readSeenFindings() (map[string]string, error) {
	seen := map[string]string{}
	data, err := os.ReadFile(seenFindingsPath())
	if os.IsNotExist(err) {
		return seen, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(data, &seen) != nil {
		// A broken file only means the notice comes once more.
		return map[string]string{}, nil
	}
	return seen, nil
}

// writeSeenFindings keeps only the current findings, so a withdrawn one that
// comes back is shown again.
func writeSeenFindings(findings []securityFinding) error {
	seen := map[string]string{}
	for _, f := range findings {
		if f.ID != "" {
			seen[f.ID] = f.Modified
		}
	}
	data, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	path := seenFindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
