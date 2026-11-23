package credential

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/termout"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/rootfile"
)

var usernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

func Path(username string) (string, error) {
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("invalid username: %q", username)
	}
	return filepath.Join("/var/lib/gjallarOS/passwords", username+".hash"), nil
}

func Exists(ctx context.Context, path string) bool {
	return exec.CommandContext(ctx, "sudo", "test", "-s", path).Run() == nil
}

// Load reads a stored hash back, for applying it to an account that already
// exists.
func Load(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "sudo", "cat", "--", path).Output()
	if err != nil {
		return "", fmt.Errorf("read stored password hash %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// AccountStatus tells whether username exists on this host and can log in
// with a password. A locked (L) or empty (NP) password is not usable.
func AccountStatus(ctx context.Context, username string) (exists, usable bool, err error) {
	if !usernamePattern.MatchString(username) {
		return false, false, fmt.Errorf("invalid username: %q", username)
	}
	if exec.CommandContext(ctx, "getent", "passwd", username).Run() != nil {
		return false, false, nil
	}
	out, err := exec.CommandContext(ctx, "sudo", "passwd", "-S", username).Output()
	if err != nil {
		return true, false, fmt.Errorf("password status of %s: %w", username, err)
	}
	return true, passwordUsable(string(out)), nil
}

func passwordUsable(status string) bool {
	fields := strings.Fields(status)
	return len(fields) > 1 && fields[1] == "P"
}

func ReadConfirmedPassword(tty *os.File, out io.Writer, username string) (string, error) {
	reader := bufio.NewReader(tty)
	for {
		first, err := ReadSecret(tty, reader, out, fmt.Sprintf("Password for %s: ", username))
		if err != nil {
			return "", err
		}
		if first == "" {
			fmt.Fprintln(out, "Password must not be empty.")
			continue
		}
		second, err := ReadSecret(tty, reader, out, "Confirm password: ")
		if err != nil {
			return "", err
		}
		if first != second {
			fmt.Fprintln(out, "Passwords do not match.")
			continue
		}
		return first, nil
	}
}

// ErrInterrupted reports Ctrl-C (or SIGTERM, SIGHUP) at a hidden prompt.
var ErrInterrupted = errors.New("interrupted")

// ReadSecret reads one line with terminal echo off. A signal while it waits
// ends the read: being killed there left the terminal without echo.
func ReadSecret(tty *os.File, reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	fmt.Fprint(out, prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = tty
	if err := off.Run(); err != nil {
		return "", fmt.Errorf("disable terminal echo: %w", err)
	}
	defer func() { on := exec.Command("stty", "echo"); on.Stdin = tty; _ = on.Run() }()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case r := <-done:
		fmt.Fprintln(out)
		return strings.TrimRight(r.line, "\r\n"), r.err
	case <-signals:
		fmt.Fprintln(out)
		return "", ErrInterrupted
	}
}

func Hash(ctx context.Context, password string) (string, error) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("mkpasswd"); err == nil {
		cmd = exec.CommandContext(ctx, "mkpasswd", "--method=yescrypt", "--stdin")
	} else if _, err := exec.LookPath("openssl"); err == nil {
		cmd = exec.CommandContext(ctx, "openssl", "passwd", "-6", "-stdin")
	} else {
		return "", fmt.Errorf("mkpasswd or openssl is required")
	}
	cmd.Stdin = strings.NewReader(password)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Apply sets the current password hash of an existing account. With
// users.mutableUsers=true NixOS only uses hashedPasswordFile when it creates an
// account, so a stored hash never reaches an account that already exists.
func Apply(ctx context.Context, username, hash string) error {
	line, err := chpasswdLine(username, hash)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sudo", "chpasswd", "--encrypted")
	cmd.Stdin = strings.NewReader(line)
	_, cmd.Stderr = termout.Output()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apply password hash for %s: %w", username, err)
	}
	return nil
}

func chpasswdLine(username, hash string) (string, error) {
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("invalid username: %q", username)
	}
	if !strings.HasPrefix(hash, "$") || strings.ContainsAny(hash, ":\r\n") {
		return "", fmt.Errorf("refusing malformed password hash for %s", username)
	}
	return username + ":" + hash + "\n", nil
}

func Store(ctx context.Context, target, hash string) error {
	cmd := exec.CommandContext(ctx, "sudo", storeDirCommand(target)...)
	cmd.Stdin = os.Stdin
	if err := termout.Attach(cmd); err != nil {
		return fmt.Errorf("store password hash: %w", err)
	}
	// The hash goes to root through a pipe, never through a file (L54).
	if err := rootfile.Write(ctx, []byte(hash+"\n"), "0600", target); err != nil {
		return fmt.Errorf("store password hash: %w", err)
	}
	return nil
}

// storeDirCommand makes the password folder root-only. `install -D` alone
// would create it 0755 and expose which accounts have stored hashes;
// `install -d` also tightens a folder an earlier version created.
func storeDirCommand(target string) []string {
	return []string{"install", "-d", "-m", "0700", "-o", "root", "-g", "root", "--", filepath.Dir(target)}
}
