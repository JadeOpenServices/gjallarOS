package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/secureboot"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/sourcerevision"
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
	// A character device is not enough: /dev/null is one (e2e-r292,
	// 2026-11-10). Only a terminal answers TCGETS.
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
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
}

// offerInstallReport shows the report and saves it only after two yes
// answers, both default No. The saved file holds exactly the bytes shown.
// Nothing is sent: the user mails the file themselves.
func offerInstallReport(ctx context.Context, ui prompt.UI, out io.Writer, input reportInput) {
	switch input.reason {
	case installreport.ReasonFailed:
		fmt.Fprintf(out, "\nGjallarOS can show a report about this machine and the error above. "+
			"You decide whether to save it and send it to %s. Nothing is sent from here.\n", installreport.Contact)
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

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(out, "WARNING: install report not saved: %v\n", err)
		return
	}
	save, err := ui.Confirm(ctx, fmt.Sprintf("Save this report as %s in %s?", installreport.FileName(now), home), false)
	if err != nil || !save {
		fmt.Fprintln(out, "Install report not saved.")
		return
	}
	path, err := installreport.Save(home, data, now)
	if err != nil {
		fmt.Fprintf(out, "WARNING: install report not saved: %v\n", err)
		return
	}
	fmt.Fprintf(out, "Install report saved to %s (readable by you only).\n"+
		"Copy it to a USB stick or another computer, read it once more, and send it to %s.\n",
		path, installreport.Contact)
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
		host, _ := os.Hostname()
		known := installreport.Known{
			User:     input.user.Username,
			Name:     input.user.Name,
			Host:     input.user.Hostname,
			Email:    input.user.Email,
			GitHub:   input.user.GitHubUsername,
			LiveHost: host,
			LiveUser: os.Getenv("USER"),
			Serials:  installreport.DiskSerials("/"),
		}
		raw := input.failure.Error()
		report.Error = installreport.ErrorText(raw, known)
	}
	return report
}
