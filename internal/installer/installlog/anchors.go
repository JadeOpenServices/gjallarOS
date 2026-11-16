package installlog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ID is an anchor code. A code never changes and is never reused: docs,
// bug reports and old logs point at it. Retire an anchor by keeping its
// row with Retired set.
type ID uint16

// Installer anchors, GJA-1000 to GJA-1999; the installer stick's menu,
// GJA-2000 to GJA-2999; the USB stick makers (make-stick.sh and
// make-stick.ps1), GJA-3000 to GJA-3999. Rebuild, update and recovery get
// their own ranges when they start logging.
const (
	RunStarted           ID = 1001
	RunHost              ID = 1002
	DiskGiven            ID = 1010
	DiskOnlyOneConfirmed ID = 1011
	DiskChosen           ID = 1012
	TargetValidated      ID = 1020
	TargetRootReady      ID = 1021
	HardwareSwapDropped  ID = 1025
	NixOSInstallStarted  ID = 1030
	NixOSInstallFinished ID = 1031
	CheckoutCloned       ID = 1040
	FreshInstallFinished ID = 1041
	SecureBootContinue   ID = 1050
	InPlaceStarted       ID = 1060
	InPlaceFinished      ID = 1061
	RunRestart           ID = 1090
	RunFailed            ID = 1098
	RunFinished          ID = 1099

	StickBootStarted      ID = 2001
	StickDisplayStarted   ID = 2010
	StickNetworkOnline    ID = 2020
	StickNetworkLookAgain ID = 2021
	StickNetworkNone      ID = 2022
	StickMenuChosen       ID = 2030
	StickChannelAdvanced  ID = 2031
	StickFlowFailed       ID = 2098

	MakerRunStarted        ID = 3001
	MakerDownloadFinished  ID = 3010
	MakerSignatureVerified ID = 3011
	MakerSignatureBad      ID = 3012
	MakerDiskChosen        ID = 3020
	MakerElevationDeclined ID = 3021
	MakerDiskBusy          ID = 3022
	MakerWriteFinished     ID = 3030
	MakerReadbackVerified  ID = 3031
	MakerReadbackMismatch  ID = 3032
	MakerWriteEnded        ID = 3033
	MakerLogsCreated       ID = 3040
	MakerLogsSkipped       ID = 3041
	MakerRunFailed         ID = 3098
	MakerRunFinished       ID = 3099
)

// Kind is the type of an anchor field. There is no free-text kind: a
// field can only carry a number, a yes/no or one of a fixed set of words,
// so nothing a user typed or a machine-specific value fits in it.
type Kind int

const (
	KindCount Kind = iota + 1
	KindBool
	KindChoice
)

func (k Kind) String() string {
	switch k {
	case KindCount:
		return "count"
	case KindBool:
		return "yes/no"
	case KindChoice:
		return "one of"
	}
	return "unknown"
}

// FieldSpec declares one field an anchor carries.
type FieldSpec struct {
	Name    string
	Kind    Kind
	Choices []string
	Meaning string
}

// Anchor is one registered point of the run.
type Anchor struct {
	ID      ID
	Name    string
	Meaning string
	Fields  []FieldSpec
	Retired bool
}

// Code is the anchor's printed code, for example GJA-1011.
func (id ID) Code() string { return "GJA-" + strconv.Itoa(int(id)) }

// Registry is every anchor ever used. Docs come from it
// (docs/install-log-anchors.md), and a test fails on an ID constant that
// is missing here.
var Registry = []Anchor{
	{ID: RunStarted, Name: "install.run.started",
		Meaning: "The installer started and the log opened."},
	{ID: RunHost, Name: "install.run.host",
		Meaning: "The installer found out what kind of system it runs on.",
		Fields: []FieldSpec{{Name: "kind", Kind: KindChoice, Choices: []string{"live", "installed", "recovery"},
			Meaning: "live media, an installed gjallarOS, or the recovery system"}}},
	{ID: DiskGiven, Name: "install.disk.given",
		Meaning: "The target disk came from --target-disk; nothing was asked."},
	{ID: DiskOnlyOneConfirmed, Name: "install.disk.only-one-confirmed",
		Meaning: "Only one disk qualified and the user said yes to it."},
	{ID: DiskChosen, Name: "install.disk.chosen",
		Meaning: "The user picked the target from several disks.",
		Fields:  []FieldSpec{{Name: "disks", Kind: KindCount, Meaning: "how many disks qualified"}}},
	{ID: TargetValidated, Name: "install.target.validated",
		Meaning: "The target disk passed the safety checks; the disk plan is next."},
	{ID: TargetRootReady, Name: "install.target.root-ready",
		Meaning: "The new root file system is mounted at /mnt.",
		Fields:  []FieldSpec{{Name: "encrypted", Kind: KindBool, Meaning: "root is inside LUKS"}}},
	{ID: HardwareSwapDropped, Name: "install.hardware.disk-swap-dropped",
		Meaning: "Swap partitions that were on while the hardware configuration was made are left out; gjallarOS swaps to zram only. The partitions are not changed.",
		Fields:  []FieldSpec{{Name: "swaps", Kind: KindCount, Meaning: "how many swap partitions were left out"}}},
	{ID: NixOSInstallStarted, Name: "install.nixos-install.started",
		Meaning: "nixos-install started building and copying the system."},
	{ID: NixOSInstallFinished, Name: "install.nixos-install.finished",
		Meaning: "nixos-install finished without an error."},
	{ID: CheckoutCloned, Name: "install.checkout.cloned",
		Meaning: "The system checkout was cloned into the target."},
	{ID: FreshInstallFinished, Name: "install.fresh.finished",
		Meaning: "The fresh install on the new disk is complete; Secure Boot steps may follow."},
	{ID: SecureBootContinue, Name: "install.secureboot.continue",
		Meaning: "The Secure Boot part of the run started.",
		Fields: []FieldSpec{{Name: "path", Kind: KindChoice, Choices: []string{"shim", "shim-fresh", "shim-sb-off", "own-keys"},
			Meaning: "shim on a running system, shim after a fresh install from the stick, shim while firmware Secure Boot is off, or own keys"}}},
	{ID: InPlaceStarted, Name: "install.inplace.started",
		Meaning: "The in-place install on the running system started building.",
		Fields: []FieldSpec{{Name: "path", Kind: KindChoice, Choices: []string{"first", "system-checkout", "repo"},
			Meaning: "first install with a new system checkout, an update of the system checkout, or a build from the installer's own checkout"}}},
	{ID: InPlaceFinished, Name: "install.inplace.finished",
		Meaning: "The in-place build and switch finished without an error."},
	{ID: RunRestart, Name: "install.run.restart",
		Meaning: "The installer restarts the machine to go on.",
		Fields: []FieldSpec{{Name: "into", Kind: KindChoice, Choices: []string{"system", "firmware-setup"},
			Meaning: "gjallarOS or the firmware setup screen"}}},
	{ID: RunFailed, Name: "install.run.failed",
		Meaning: "The run stopped with an error; the ERROR line above says why."},
	{ID: RunFinished, Name: "install.run.finished",
		Meaning: "The run ended.",
		Fields:  []FieldSpec{{Name: "code", Kind: KindCount, Meaning: "exit code: 0 done, 1 failed, 2 wrong options, 130 stopped by the user"}}},

	{ID: StickBootStarted, Name: "stick.boot.started",
		Meaning: "The installer stick's menu started for the first time this boot.",
		Fields:  []FieldSpec{{Name: "secureboot", Kind: KindBool, Meaning: "the firmware booted with Secure Boot on"}}},
	{ID: StickDisplayStarted, Name: "stick.display.started",
		Meaning: "The stick's menu is on the screen.",
		Fields: []FieldSpec{{Name: "mode", Kind: KindChoice, Choices: []string{"window", "console"},
			Meaning: "a window on the screen, or the text console"}}},
	{ID: StickNetworkOnline, Name: "stick.network.online",
		Meaning: "The stick has a network connection.",
		Fields: []FieldSpec{{Name: "via", Kind: KindChoice, Choices: []string{"ethernet", "wifi"},
			Meaning: "a cable or Wi-Fi"}}},
	{ID: StickNetworkLookAgain, Name: "stick.network.look-again",
		Meaning: "The user asked the stick to look for a network again.",
		Fields:  []FieldSpec{{Name: "tries", Kind: KindCount, Meaning: "how many times so far"}}},
	{ID: StickNetworkNone, Name: "stick.network.none",
		Meaning: "The user went on without a network connection."},
	{ID: StickMenuChosen, Name: "stick.menu.chosen",
		Meaning: "The user picked an entry of the stick's menu.",
		Fields: []FieldSpec{{Name: "choice", Kind: KindChoice, Choices: []string{"install", "move-disk", "bring-over", "recovery", "shell"},
			Meaning: "install, move to a new disk, bring an old system over, recovery, or the shell"}}},
	{ID: StickChannelAdvanced, Name: "stick.channel.advanced",
		Meaning: "The user changed the release channel under Advanced.",
		Fields: []FieldSpec{{Name: "channel", Kind: KindChoice, Choices: []string{"stable", "staging", "dev"},
			Meaning: "the channel the install will follow"}}},
	{ID: StickFlowFailed, Name: "stick.flow.failed",
		Meaning: "A step of the stick's menu failed; the ERROR line above says why."},

	{ID: MakerRunStarted, Name: "maker.run.started",
		Meaning: "The USB stick maker started.",
		Fields: []FieldSpec{{Name: "os", Kind: KindChoice, Choices: []string{"linux", "windows"},
			Meaning: "the system the maker runs on"}}},
	{ID: MakerDownloadFinished, Name: "maker.download.finished",
		Meaning: "The stick image and its checksum files are downloaded."},
	{ID: MakerSignatureVerified, Name: "maker.signature.verified",
		Meaning: "The signature and every checksum the maker needs are correct."},
	{ID: MakerSignatureBad, Name: "maker.signature.bad",
		Meaning: "The signature or a checksum did not match; the download was deleted."},
	{ID: MakerDiskChosen, Name: "maker.disk.chosen",
		Meaning: "The user picked the USB stick to write.",
		Fields:  []FieldSpec{{Name: "disks", Kind: KindCount, Meaning: "how many USB sticks the maker offered"}}},
	{ID: MakerElevationDeclined, Name: "maker.elevation.declined",
		Meaning: "Admin rights for the write were refused or not available; nothing was written."},
	{ID: MakerDiskBusy, Name: "maker.disk.busy",
		Meaning: "Windows could not lock a volume on the stick because a program has it open; nothing was written and the maker offers to try again."},
	{ID: MakerWriteFinished, Name: "maker.write.finished",
		Meaning: "The image is written to the stick."},
	{ID: MakerReadbackVerified, Name: "maker.readback.verified",
		Meaning: "The stick reads back the same bytes that were signed."},
	{ID: MakerReadbackMismatch, Name: "maker.readback.mismatch",
		Meaning: "The stick read back different bytes; its first MiB was wiped so it does not boot."},
	{ID: MakerWriteEnded, Name: "maker.write.ended",
		Meaning: "The admin process for the write ended without one of its own result codes, for example because Windows Security stopped it; the maker does not count the stick as written.",
		Fields:  []FieldSpec{{Name: "code", Kind: KindCount, Meaning: "the exit code Windows reported"}}},
	{ID: MakerLogsCreated, Name: "maker.logs.created",
		Meaning: "The GJLOGS partition for install logs is on the stick."},
	{ID: MakerLogsSkipped, Name: "maker.logs.skipped",
		Meaning: "The stick got no GJLOGS partition; the installer offers to save logs to another drive instead.",
		Fields: []FieldSpec{{Name: "reason", Kind: KindChoice, Choices: []string{"too-small", "layout"},
			Meaning: "the stick has no room after the image, or the image's partition table leaves no free slot"}}},
	{ID: MakerRunFailed, Name: "maker.run.failed",
		Meaning: "A step of the maker failed; the ERROR line above says why."},
	{ID: MakerRunFinished, Name: "maker.run.finished",
		Meaning: "The maker ended.",
		Fields:  []FieldSpec{{Name: "code", Kind: KindCount, Meaning: "exit code: 0 done, 1 failed, 2 wrong options, 130 stopped by the user"}}},
}

var byID = func() map[ID]Anchor {
	m := make(map[ID]Anchor, len(Registry))
	for _, a := range Registry {
		m[a.ID] = a
	}
	return m
}()

// Lookup returns the registered anchor for id.
func Lookup(id ID) (Anchor, bool) {
	a, ok := byID[id]
	return a, ok
}

// Field is one value written with an anchor.
type Field struct {
	name  string
	kind  Kind
	value string
}

// Count is a number field.
func Count(name string, n int) Field {
	return Field{name: name, kind: KindCount, value: strconv.Itoa(n)}
}

// Bool is a yes/no field.
func Bool(name string, b bool) Field {
	return Field{name: name, kind: KindBool, value: strconv.FormatBool(b)}
}

// Choice is a field holding one of the anchor's declared words.
func Choice(name, value string) Field {
	return Field{name: name, kind: KindChoice, value: value}
}

// Line is the log text for an anchor. A field the anchor does not declare,
// or a choice outside its set, is written as <invalid> rather than as its
// value; a test keeps that from happening.
func Line(id ID, fields ...Field) (string, error) {
	a, ok := byID[id]
	if !ok {
		return id.Code() + " unregistered", fmt.Errorf("anchor %s is not registered", id.Code())
	}
	var b strings.Builder
	b.WriteString(a.ID.Code())
	b.WriteString(" ")
	b.WriteString(a.Name)
	var bad error
	for _, f := range fields {
		value := f.value
		if err := a.check(f); err != nil {
			value = "<invalid>"
			if bad == nil {
				bad = err
			}
		}
		b.WriteString(" " + f.name + "=" + value)
	}
	return b.String(), bad
}

func (a Anchor) check(f Field) error {
	for _, spec := range a.Fields {
		if spec.Name != f.name {
			continue
		}
		if spec.Kind != f.kind {
			return fmt.Errorf("%s field %s is %s, not %s", a.ID.Code(), f.name, spec.Kind, f.kind)
		}
		if spec.Kind == KindChoice {
			for _, c := range spec.Choices {
				if c == f.value {
					return nil
				}
			}
			return fmt.Errorf("%s field %s has no choice %q", a.ID.Code(), f.name, f.value)
		}
		return nil
	}
	return fmt.Errorf("%s has no field %s", a.ID.Code(), f.name)
}

// Mark writes an anchor line to the current log. It does nothing when no
// log is open.
func Mark(id ID, fields ...Field) {
	Current().Mark(id, fields...)
}

// Mark writes an anchor line to l.
func (l *Log) Mark(id ID, fields ...Field) {
	if l == nil {
		return
	}
	line, _ := Line(id, fields...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(line, true)
}

// Markdown is the anchor reference, docs/install-log-anchors.md.
func Markdown() string {
	anchors := append([]Anchor(nil), Registry...)
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].ID < anchors[j].ID })
	var b strings.Builder
	b.WriteString("# Install log anchors\n\n")
	b.WriteString("<!-- Generated from internal/installer/installlog/anchors.go. ")
	b.WriteString("Update with: GJALLAR_UPDATE_DOCS=1 go test ./internal/installer/installlog -->\n\n")
	b.WriteString("The installer, the installer stick's menu and the USB stick makers write\n")
	b.WriteString("a log of each run.\n")
	b.WriteString("Fixed points of the run carry an anchor: a code that never changes and a\n")
	b.WriteString("name that says what happened, for example\n")
	b.WriteString("`GJA-1011 install.disk.only-one-confirmed`. Search a log for the code to\n")
	b.WriteString("find where a run got to. Codes GJA-1000 to GJA-1999 come from the\n")
	b.WriteString("installer, GJA-2000 to GJA-2999 from the stick's menu, GJA-3000 to\n")
	b.WriteString("GJA-3999 from the USB stick makers. Anchor fields only hold numbers,\n")
	b.WriteString("yes/no or one of the words listed here, never text a user typed.\n\n")
	b.WriteString("| Code | Name | Meaning | Fields |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, a := range anchors {
		meaning := a.Meaning
		if a.Retired {
			meaning = "Retired. " + meaning
		}
		var fields []string
		for _, f := range a.Fields {
			text := "`" + f.Name + "` (" + f.Kind.String()
			if f.Kind == KindChoice {
				text += " " + strings.Join(f.Choices, ", ")
			}
			text += "): " + f.Meaning
			fields = append(fields, text)
		}
		fieldText := "none"
		if len(fields) > 0 {
			fieldText = strings.Join(fields, "<br>")
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", a.ID.Code(), a.Name, meaning, fieldText)
	}
	return b.String()
}
