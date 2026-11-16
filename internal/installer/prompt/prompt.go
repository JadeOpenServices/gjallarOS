package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JadeOpenServices/gjallarOS/internal/tty"
)

var ErrCancelled = errors.New("prompt cancelled")

type UI struct {
	Reader *bufio.Reader
	Out    io.Writer
	GTK    bool
}

func zenityCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "zenity", args...)

	// The bootstrap/base NixOS environment may expose a locale which GTK
	// cannot use. Zenity then falls back to plain C and exits 255 as soon
	// as UTF-8 text such as bullets or em-dashes is rendered.
	//
	// Preserve the graphical/session environment, but replace LANG and
	// LC_ALL with a known UTF-8 locale.
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LANG=") ||
			strings.HasPrefix(entry, "LC_ALL=") {
			continue
		}
		env = append(env, entry)
	}

	cmd.Env = append(
		env,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	)

	return cmd
}

var geteuid = os.Geteuid

func gtkAvailable() bool {
	_, err := exec.LookPath("zenity")
	return err == nil &&
		(os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") &&
		os.Getenv("SSH_CONNECTION") == "" &&
		!sudoFromDesktop()
}

// sudo keeps WAYLAND_DISPLAY, but the session belongs to the user who ran
// sudo and root's zenity cannot open it. Every zenity failure reads as a
// cancel, so `sudo install.sh` stopped with "prompt cancelled" before the
// first question (e2e-fw13 2026-10-20). Ask in the terminal instead.
func sudoFromDesktop() bool {
	return geteuid() == 0 && os.Getenv("SUDO_USER") != ""
}

func gtkConfirmationWidth(message string) int {
	normalized := strings.ReplaceAll(message, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	totalRunes := 0
	longestParagraph := 0

	for _, paragraph := range strings.Split(normalized, "\n\n") {
		length := utf8.RuneCountInString(strings.TrimSpace(paragraph))
		totalRunes += length
		if length > longestParagraph {
			longestParagraph = length
		}
	}

	switch {
	case totalRunes <= 90 && longestParagraph <= 90:
		return 420
	case totalRunes <= 240 && longestParagraph <= 180:
		return 520
	case totalRunes <= 600 && longestParagraph <= 400:
		return 680
	default:
		return 760
	}
}

// terminalWriter is a writer that copies output somewhere else, such as the
// install log. Terminal is the writer behind it.
type terminalWriter interface {
	Terminal() io.Writer
}

// terminal returns the writer behind any logging writers around out.
func terminal(out io.Writer) io.Writer {
	for {
		t, ok := out.(terminalWriter)
		if !ok {
			return out
		}
		out = t.Terminal()
	}
}

func New(in io.Reader, out io.Writer) UI {
	gtk := false

	input, inputOK := in.(*os.File)
	output, outputOK := terminal(out).(*os.File)
	if inputOK && outputOK &&
		input == os.Stdin &&
		(output == os.Stdout || output == os.Stderr) {
		gtk = gtkAvailable()
	}

	return UI{bufio.NewReader(in), out, gtk}
}

func (u UI) secureTextDialog(
	ctx context.Context,
	title string,
	text string,
	okLabel string,
) error {
	if !u.GTK {
		fmt.Fprintln(u.Out)
		fmt.Fprintln(u.Out, text)
		fmt.Fprintln(u.Out)
		return nil
	}

	tmp, err := os.CreateTemp("", "gjallar-dialog-*.txt")
	if err != nil {
		return fmt.Errorf("create GTK text file: %w", err)
	}

	name := tmp.Name()
	defer os.Remove(name)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect GTK text file: %w", err)
	}

	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return fmt.Errorf("write GTK text file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync GTK text file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close GTK text file: %w", err)
	}

	cmd := zenityCommand(
		ctx,
		"--text-info",
		"--title="+title,
		"--width=760",
		"--height=680",
		"--filename="+name,
		"--ok-label="+okLabel,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())

		if exit, ok := err.(*exec.ExitError); ok {
			if msg == "" {
				msg = err.Error()
			}

			return fmt.Errorf(
				"GTK dialog %q failed with exit %d: %s",
				title,
				exit.ExitCode(),
				msg,
			)
		}

		return fmt.Errorf("launch GTK dialog %q: %w", title, err)
	}

	return nil
}

func (u UI) Confirm(ctx context.Context, message string, defaultYes bool) (bool, error) {
	if u.GTK {
		width := gtkConfirmationWidth(message)
		args := []string{
			"--question",
			"--title=gjallarOS installer",
			"--text=" + message,
			"--width=" + strconv.Itoa(width),
			"--ok-label=Yes",
			"--cancel-label=No",
		}
		err := zenityCommand(ctx, args...).Run()
		if err == nil {
			return true, nil
		}
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, ErrCancelled
	}
	suffix := " [y/N] "
	if defaultYes {
		suffix = " [Y/n] "
	}
	fmt.Fprint(u.Out, message+suffix)
	line, err := u.readLine(ctx)
	if err != nil {
		return false, err
	}
	value := strings.ToLower(strings.TrimSpace(line))
	if value == "" {
		return defaultYes, nil
	}
	return value == "y" || value == "yes", nil
}

// SecureBootFirmwareHandoff presents the security-sensitive firmware
// ownership-transfer instructions as GTK dialogs when GTK/Zenity is
// available. The user must explicitly understand the instructions and then
// decline another review before the installer may reboot into firmware.
// ShowSecureBootRecovery displays the generated recovery material before
// the installer asks the user to confirm that it has been saved.
//
// In GTK mode the secret is passed to Zenity through a mode-0600 temporary
// file instead of command-line arguments, avoiding exposure through argv.
func (u UI) ShowSecureBootRecovery(
	ctx context.Context,
	archivePath string,
	passphrase string,
) error {
	if archivePath == "" || passphrase == "" {
		return fmt.Errorf("Secure Boot recovery material is incomplete")
	}

	text := fmt.Sprintf(`GJALLAROS SECURE BOOT RECOVERY MATERIAL

SAVE BOTH ITEMS BELOW BEFORE CONTINUING.


SECURE BOOT RECOVERY ARCHIVE

%s


SECURE BOOT RECOVERY PASSPHRASE

%s


IMPORTANT

The encrypted archive contains this machine's gjallarOS Secure Boot
signing keys.

The recovery passphrase is required to decrypt that archive.

Store BOTH items somewhere safe and offline.

Do not continue until you have saved both.
`, archivePath, passphrase)

	if !u.GTK {
		out, done, err := u.secretOut()
		if err != nil {
			return err
		}
		defer done()
		fmt.Fprintln(out)
		fmt.Fprintln(out, text)
		fmt.Fprintln(out)
		return nil
	}
	return u.secureTextDialog(
		ctx,
		"gjallarOS Secure Boot Recovery Key",
		text,
		"I have saved this recovery material",
	)
}

// secretOut returns where secret recovery material may be shown. When the
// output file is not a terminal it is a log: installer-resume.service sends
// stdout to the journal, which kept the Secure Boot recovery passphrase
// (e2e-full, 2026-10-05). The secret then goes to the controlling terminal
// only, or nowhere. A writer that logs (the install log) is skipped: the
// secret goes to the terminal behind it and never into the log.
func (u UI) secretOut() (io.Writer, func(), error) {
	out := terminal(u.Out)
	file, ok := out.(*os.File)
	if !ok {
		return out, func() {}, nil
	}
	if tty.Is(file) {
		return out, func() {}, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("Secure Boot recovery material needs a terminal; output is not one: %w", err)
	}
	return tty, func() { tty.Close() }, nil
}

// SecureBootOwnershipPlan shows what the Secure Boot ownership transfer
// changes before the user decides on it.
func (u UI) SecureBootOwnershipPlan(ctx context.Context, plan string) error {
	return u.secureTextDialog(
		ctx,
		"gjallarOS Secure Boot - What Will Change",
		plan,
		"Continue",
	)
}

func (u UI) SecureBootEnableHandoff(
	ctx context.Context,
	firmwareName string,
	lockSteps []string,
) error {
	firmwareName = strings.TrimSpace(firmwareName)
	if firmwareName == "" {
		firmwareName = "system firmware"
	}

	instructions := fmt.Sprintf(`GJALLAROS SECURE BOOT
FINAL FIRMWARE ACTION

gjallarOS ownership is already enrolled and verified.

Firmware: %s

%s
Do not clear, erase, reset, or replace Secure Boot keys during this final step.
`, firmwareName, numberedSteps(finalFirmwareSteps(lockSteps)))

	if err := u.secureTextDialog(
		ctx,
		"gjallarOS Secure Boot - Enable Secure Boot",
		instructions,
		"Continue",
	); err != nil {
		return err
	}

	understood, err := u.Confirm(
		ctx,
		"Enable Secure Boot without clearing or replacing any keys?",
		false,
	)
	if err != nil {
		return err
	}
	if !understood {
		return fmt.Errorf("Secure Boot enable instructions were not acknowledged")
	}

	return nil
}

func (u UI) SecureBootFirmwareHandoff(
	ctx context.Context,
	firmwareName string,
	policyInstructions []string,
	lockSteps []string,
) error {
	firmwareName = strings.TrimSpace(firmwareName)
	if firmwareName == "" {
		firmwareName = "system firmware"
	}

	clean := make([]string, 0, len(policyInstructions))
	for _, instruction := range policyInstructions {
		instruction = strings.TrimSpace(instruction)
		if instruction != "" {
			clean = append(clean, instruction)
		}
	}

	if len(clean) == 0 {
		return fmt.Errorf("Secure Boot firmware policy contains no display instructions")
	}

	instructions := fmt.Sprintf(`GJALLAROS SECURE BOOT
FIRMWARE ACTION REQUIRED

Firmware: %s

Follow the detected device firmware policy exactly:

%s

After completing the firmware action:

- save the firmware configuration
- boot gjallarOS normally
- leave further Secure Boot changes to the gjallarOS enrollment transaction

gjallarOS will verify the expected firmware state before performing enrollment.
After enrolling its keys it reboots into firmware setup once more. There:

%s`,
		firmwareName,
		strings.Join(clean, "\n"),
		numberedSteps(finalFirmwareSteps(lockSteps)),
	)

	explanation := `WHY THIS IS DEVICE-SPECIFIC

Different firmware implementations require different steps to enter a safe
Secure Boot enrollment state.

gjallarOS therefore does not hardcode a vendor procedure in the installer UI.
The instructions above come from the detected device's validated ODDC firmware
policy.

The policy is descriptive data only. It cannot provide commands, scripts,
executables, URLs, or arbitrary shell input.

gjallarOS will independently verify the resulting firmware state before any
key enrollment is allowed.
`

	for {
		if err := u.secureTextDialog(
			ctx,
			"gjallarOS Secure Boot - Firmware Instructions",
			instructions,
			"Continue",
		); err != nil {
			return err
		}

		understood, err := u.Confirm(
			ctx,
			"Do you understand the Secure Boot firmware instructions?",
			false,
		)
		if err != nil {
			return err
		}

		if !understood {
			if err := u.secureTextDialog(
				ctx,
				"gjallarOS Secure Boot - Explanation",
				explanation,
				"Back to instructions",
			); err != nil {
				return err
			}

			continue
		}

		showAgain, err := u.Confirm(
			ctx,
			"Would you like to see the previous instructions again?",
			false,
		)
		if err != nil {
			return err
		}

		if showAgain {
			continue
		}

		return nil
	}
}

func (u UI) Exact(ctx context.Context, label, expected string) error {
	if expected == "" {
		return fmt.Errorf("exact confirmation value must not be empty")
	}

	var value string

	if u.GTK {
		out, err := zenityCommand(
			ctx,
			"--entry",
			"--title=gjallarOS installer",
			"--text="+label,
		).Output()
		if err != nil {
			return ErrCancelled
		}
		value = strings.TrimSuffix(string(out), "\n")
		value = strings.TrimSuffix(value, "\r")
	} else {
		fmt.Fprintf(u.Out, "%s: ", label)
		line, err := u.readLine(ctx)
		if err != nil {
			return err
		}
		value = strings.TrimSuffix(line, "\n")
		value = strings.TrimSuffix(value, "\r")
	}

	if value != expected {
		return fmt.Errorf("destructive confirmation did not match exactly")
	}

	return nil
}

// readLine reads one answer, or gives up when ctx ends. Ctrl-C cancels ctx;
// a plain read would keep waiting and take the next line typed as the
// answer to a question the user already walked away from.
func (u UI) readLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := u.Reader.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case r := <-done:
		return r.line, r.err
	case <-ctx.Done():
		fmt.Fprintln(u.Out)
		return "", ctx.Err()
	}
}

func (u UI) Value(ctx context.Context, label, def string) (string, error) {
	if u.GTK {
		out, err := zenityCommand(ctx, "--entry", "--title=gjallarOS installer", "--text="+label, "--entry-text="+def).Output()
		if err != nil {
			return "", ErrCancelled
		}
		return strings.TrimSpace(string(out)), nil
	}
	if def == "" {
		// No default: "[]" read like an empty answer was expected.
		fmt.Fprintf(u.Out, "%s: ", label)
	} else {
		fmt.Fprintf(u.Out, "%s [%s]: ", label, def)
	}
	line, err := u.readLine(ctx)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	return line, nil
}

// Example asks for a personal value with nothing filled in; example only
// shows what an answer looks like. A required value is asked again until
// one is given.
func (u UI) Example(ctx context.Context, label, example string, required bool) (string, error) {
	text := label + " (e.g. " + example + ")"
	if !required {
		text = label + " (optional, e.g. " + example + ")"
	}
	for {
		var line string
		if u.GTK {
			out, err := zenityCommand(ctx, "--entry", "--title=gjallarOS installer", "--text="+text).Output()
			if err != nil {
				return "", ErrCancelled
			}
			line = string(out)
		} else {
			fmt.Fprintf(u.Out, "%s: ", text)
			read, err := u.readLine(ctx)
			if err != nil {
				return "", err
			}
			line = read
		}
		line = strings.TrimSpace(line)
		if line != "" || !required {
			return line, nil
		}
		if !u.GTK {
			fmt.Fprintf(u.Out, "%s is required.\n", label)
		}
	}
}

func (u UI) Choice(ctx context.Context, label, def string, options []string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options for %s", label)
	}
	if u.GTK {
		args := []string{"--list", "--radiolist", "--title=gjallarOS installer", "--text=" + label, "--column=Selected", "--column=Value"}
		for _, v := range options {
			args = append(args, strconv.FormatBool(v == def), v)
		}
		out, err := zenityCommand(ctx, args...).Output()
		if err != nil {
			return "", ErrCancelled
		}
		value := strings.TrimSpace(string(out))
		if value == "" {
			return "", ErrCancelled
		}
		return value, nil
	}
	for i, v := range options {
		fmt.Fprintf(u.Out, "  %d) %s\n", i+1, v)
	}
	for {
		value, err := u.Value(ctx, label, def)
		if err != nil {
			return "", err
		}
		for _, candidate := range options {
			if value == candidate {
				return value, nil
			}
		}
		if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(options) {
			return options[n-1], nil
		}
		fmt.Fprintln(u.Out, "Choose one listed value.")
	}
}

// pickShown caps how many matches Pick lists at once.
const pickShown = 20

// Pick chooses one value from a long list, such as all timezones. In the
// terminal, Enter takes def, an exact name is taken as typed, and any
// other text lists the names that contain it; a number then picks from that
// list, and a single match is taken at once.
func (u UI) Pick(ctx context.Context, label, def string, options []string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options for %s", label)
	}
	if u.GTK {
		return u.Choice(ctx, label, def, options)
	}
	var shown []string
	for {
		value, err := u.Value(ctx, label, def)
		if err != nil {
			return "", err
		}
		if n, e := strconv.Atoi(value); e == nil && n > 0 && n <= len(shown) {
			return shown[n-1], nil
		}
		matches := []string{}
		for _, candidate := range options {
			if candidate == value {
				return candidate, nil
			}
			if strings.EqualFold(candidate, value) {
				matches = []string{candidate}
				break
			}
			if strings.Contains(strings.ToLower(candidate), strings.ToLower(value)) {
				matches = append(matches, candidate)
			}
		}
		switch {
		case len(matches) == 1:
			fmt.Fprintf(u.Out, "Using %s.\n", matches[0])
			return matches[0], nil
		case len(matches) == 0:
			shown = nil
			fmt.Fprintf(u.Out, "Nothing matches %q. Type part of a name, such as a city or country code.\n", value)
		default:
			shown = matches
			if len(shown) > pickShown {
				shown = shown[:pickShown]
			}
			for i, v := range shown {
				fmt.Fprintf(u.Out, "  %d) %s\n", i+1, v)
			}
			if len(matches) > len(shown) {
				fmt.Fprintf(u.Out, "  ... %d more; type more letters to narrow it down.\n", len(matches)-len(shown))
			}
		}
	}
}

// finalFirmwareSteps is the firmware visit that turns Secure Boot on; the
// optional supervisor-password steps belong to it because every later
// firmware change then needs that password.
func finalFirmwareSteps(lockSteps []string) []string {
	steps := []string{"Enable Secure Boot."}
	steps = append(steps, lockSteps...)
	return append(steps, "Save the firmware configuration.", "Boot gjallarOS.")
}

func numberedSteps(steps []string) string {
	var b strings.Builder
	for i, step := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	return b.String()
}
