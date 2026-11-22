package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installlog"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/sourcerevision"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
	"github.com/JadeOpenServices/gjallarOS/internal/tty"
)

// failureRecorder is run's error writer. fail() leaves the error it
// prints here, so run can offer an install report about it (Plane 292).
type failureRecorder struct {
	io.Writer
	err error
}

func recordFailure(out io.Writer, err error) {
	if recorder, ok := out.(*failureRecorder); ok {
		recorder.err = err
	}
}

// saveFailureToBootStick writes the error text and the redacted run log to
// the GJLOGS partition of the stick this run booted from, without asking
// (Plane 388 b: the T480s stick carried only the ODDC report, no error text).
// Best effort: a warning is printed, the failure itself stands either way.
func saveFailureToBootStick(ctx context.Context, out io.Writer, runLog *logKeeper, runErr error, targetDisk string, user config.User) {
	drives, err := listSaveDrives(ctx, targetDisk)
	if err != nil {
		fmt.Fprintf(out, "WARNING: failure not saved to the stick: no drive list: %v\n", err)
		return
	}
	if os.Geteuid() == 0 {
		drives = unmountedOnly(drives)
	}
	var stick *targetdisk.SaveDrive
	for i, d := range drives {
		if d.BootStick {
			stick = &drives[i]
			break
		}
	}
	if stick == nil {
		return
	}
	now := time.Now()
	files := []savedFile{{
		name: "gjallaros-error-" + now.UTC().Format("20060102-150405") + ".txt",
		// Redacted like the report's error line: no user, host or other
		// known values of this machine land on the stick.
		data: []byte("ERROR: " + installreport.ErrorText(runErr.Error(), knownValues(user)) + "\n"),
	}}
	if logData, err := runLog.export(); err != nil {
		fmt.Fprintf(out, "WARNING: install log not saved to the stick: %v\n", err)
	} else if logData != nil {
		files = append(files, savedFile{name: "gjallaros-" + installlog.FileName(now), data: logData})
	}
	names, err := writeToDrive(ctx, targetDisk, *stick, files)
	for _, name := range names {
		fmt.Fprintf(out, "Saved %s on the installer stick (GJLOGS).\n", name)
	}
	if err != nil {
		fmt.Fprintf(out, "WARNING: failure not saved to the stick: %v\n", err)
	}
}

// reportAllowed is true when a person sits at this install: stdin is a
// terminal, and the run is not resumed, unattended or managed. Nobody is
// asked anything otherwise.
func reportAllowed(in io.Reader, user config.User) bool {
	if os.Getenv("GJALLAR_INSTALLER_RESUMED") == "1" || user.UnattendedInstall || user.Management.Enable {
		return false
	}
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return tty.Is(file)
}

// reportInput is what run knows when it offers a report.
type reportInput struct {
	reason      installreport.Reason
	repo        string
	recovery    bool
	user        config.User
	oddcModel   string
	oddcChecked bool
	failure     error
	// log is the run log; after a failure it is saved with the report.
	log *logKeeper
	// toDrive is set on live and recovery runs, which save to a USB drive
	// instead of a home folder that is gone at the next start.
	toDrive bool
	// targetDisk is the disk the run installs to, never offered as a drive.
	targetDisk string
}

// offerInstallReport shows the report, then asks once whether to save it,
// default No. After a failure the redacted run log is saved with it. The
// saved report holds exactly the bytes shown. Nothing is sent: the user
// mails the files themselves.
func offerInstallReport(ctx context.Context, ui prompt.UI, out io.Writer, input reportInput) {
	switch input.reason {
	case installreport.ReasonFailed:
		fmt.Fprintf(out, "\nGjallarOS can show a report about this machine and the error above. "+
			"You decide whether to save it with the install log and send them to %s. Nothing is sent from here.\n", installreport.Contact)
	default:
		fmt.Fprintf(out, "\nNo ODDC model matches this machine. A report of its parts helps add it; "+
			"you decide whether to save it and send it to %s. Nothing is sent from here.\n", installreport.Contact)
	}
	show, err := ui.Confirm(ctx, "Show the install report?", false)
	if err != nil || !show {
		return
	}

	now := time.Now()
	data := buildReport(input, now).Render()
	fmt.Fprintln(out, "\n----- install report: begin -----")
	out.Write(data)
	fmt.Fprintln(out, "----- install report: end -----")

	files := []savedFile{{name: installreport.FileName(now), data: data}}
	if input.reason == installreport.ReasonFailed {
		if logData, err := input.log.export(); err != nil {
			fmt.Fprintf(out, "WARNING: install log not added: %v\n", err)
		} else if logData != nil {
			files = append(files, savedFile{name: "gjallaros-" + installlog.FileName(now), data: logData})
		}
	}
	if input.toDrive {
		saveFilesToDrive(ctx, ui, out, input.targetDisk, files)
		return
	}
	saveFilesToHome(ctx, ui, out, files)
}

// saveFilesToHome saves the files in the home folder of the user who ran
// the installer, after one yes (default No).
func saveFilesToHome(ctx context.Context, ui prompt.UI, out io.Writer, files []savedFile) {
	dest, err := installreport.SaveDestination()
	if err != nil {
		fmt.Fprintf(out, "WARNING: install report not saved: %v\n", err)
		return
	}
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.name)
	}
	save, err := ui.Confirm(ctx, fmt.Sprintf("Save %s in %s?", strings.Join(names, " and "), dest.Dir), false)
	if err != nil || !save {
		fmt.Fprintln(out, "Install report not saved.")
		return
	}
	saved, err := writeFiles(dest, files)
	for _, name := range saved {
		fmt.Fprintf(out, "Saved %s (readable by its owner only).\n", filepath.Join(dest.Dir, name))
	}
	if err != nil {
		fmt.Fprintf(out, "WARNING: %v\n", err)
		if len(saved) == 0 {
			return
		}
	}
	fmt.Fprintf(out, "Copy the files to a USB stick or another computer, read them once more, and send them to %s.\n", installreport.Contact)
}

func buildReport(input reportInput, now time.Time) installreport.Report {
	report := installreport.Collect("/", secureboot.ReadModeFlags)
	report.Reason = input.reason
	report.Date = now
	report.Commit, _ = sourcerevision.Resolve(input.repo, input.recovery)
	report.Channel = input.user.Channel()
	report.ODDCModel = input.oddcModel
	report.ODDCChecked = input.oddcChecked
	if input.failure != nil {
		report.Error = installreport.ErrorText(input.failure.Error(), knownValues(input.user))
	}
	return report
}
