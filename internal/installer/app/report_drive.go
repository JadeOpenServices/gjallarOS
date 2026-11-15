package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/installreport"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/targetdisk"
)

// savedFile is one file a report step saves: the report, and after a
// failure the redacted run log.
type savedFile struct {
	name string
	data []byte
}

// Tests replace the drive list and its check before mounting.
var (
	listSaveDrives = targetdisk.SaveDrives
	checkSaveDrive = targetdisk.CheckSaveDrive
)

const (
	saveLookAgain = "Look again"
	saveNot       = "Do not save"
)

// saveMountPrefix starts the folder root mounts a chosen drive at. /run
// belongs to root, so the user session cannot plant anything there. Tests
// point it at their own folder.
var saveMountPrefix = "/run/gjallar-save."

// saveFilesToDrive asks once where the files go: a USB drive from the
// list, or nowhere (default). A live system forgets its home folder at the
// next start, so live runs save to a drive (Plane 343). target is the disk
// the run installs to, never offered.
func saveFilesToDrive(ctx context.Context, ui prompt.UI, out io.Writer, target string, files []savedFile) {
	question := "Save the report to a USB drive?"
	if len(files) > 1 {
		question = "Save the report and the install log to a USB drive?"
	}
	for {
		drives, err := listSaveDrives(ctx, target)
		if err != nil {
			fmt.Fprintf(out, "WARNING: no drive list: %v\n", err)
		}
		if os.Geteuid() == 0 {
			// A desktop mount belongs to its user; root does not write
			// into a folder that user controls.
			drives = unmountedOnly(drives)
		}
		options := make([]string, 0, len(drives)+2)
		byLabel := make(map[string]targetdisk.SaveDrive, len(drives))
		for _, drive := range drives {
			options = append(options, drive.Label)
			byLabel[drive.Label] = drive
		}
		if len(drives) == 0 {
			fmt.Fprintln(out, "No USB drive with a FAT or exFAT partition found. Plug one in and choose \""+saveLookAgain+"\".")
		}
		options = append(options, saveLookAgain, saveNot)
		choice, err := ui.Choice(ctx, question, saveNot, options)
		if err != nil || choice == saveNot {
			fmt.Fprintln(out, "Install report not saved.")
			return
		}
		if choice == saveLookAgain {
			continue
		}
		drive := byLabel[choice]
		names, err := writeToDrive(ctx, target, drive, files)
		for _, name := range names {
			fmt.Fprintf(out, "Saved %s on %s.\n", name, drive.Path)
		}
		if err != nil {
			fmt.Fprintf(out, "WARNING: %v\n", err)
			if len(names) == 0 {
				continue
			}
		}
		fmt.Fprintf(out, "Remove the drive, read the files on another computer once more, and send them to %s.\n", installreport.Contact)
		return
	}
}

func unmountedOnly(drives []targetdisk.SaveDrive) []targetdisk.SaveDrive {
	var out []targetdisk.SaveDrive
	for _, drive := range drives {
		if drive.MountPoint == "" {
			out = append(out, drive)
		}
	}
	return out
}

// writeToDrive writes files to drive and returns the names it wrote. A
// drive the desktop mounted for this user gets them there, with no root
// step. Any other drive is mounted by root at a fresh folder in /run, with
// the user as the owner of every file on it, so the files themselves are
// written by the user, then unmounted again.
func writeToDrive(ctx context.Context, target string, drive targetdisk.SaveDrive, files []savedFile) ([]string, error) {
	if err := checkSaveDrive(ctx, target, drive); err != nil {
		return nil, err
	}
	owner, err := installreport.SaveDestination()
	if err != nil {
		return nil, err
	}
	if drive.MountPoint != "" {
		if os.Geteuid() == 0 {
			return nil, fmt.Errorf("%s is mounted by the desktop for its user; root does not write there", drive.Path)
		}
		return writeFiles(installreport.Destination{Dir: drive.MountPoint, UID: -1, GID: -1}, files)
	}

	uid, gid := owner.UID, owner.GID
	if uid < 0 {
		uid, gid = os.Getuid(), os.Getgid()
	}
	dir, err := makeSaveMountPoint(ctx)
	if err != nil {
		return nil, err
	}
	options := fmt.Sprintf("rw,nosuid,nodev,noexec,uid=%d,gid=%d,fmask=0177,dmask=0077", uid, gid)
	if _, err := privilegedCommand(ctx, "mount", "-t", drive.FSType, "-o", options, "--", drive.Path, dir); err != nil {
		_, _ = privilegedCommand(context.WithoutCancel(ctx), "rmdir", "--", dir)
		return nil, fmt.Errorf("mount %s: %w", drive.Path, err)
	}
	names, err := writeFiles(installreport.Destination{Dir: dir, UID: owner.UID, GID: owner.GID}, files)
	// The files are only safe on the drive once it is unmounted.
	cleanup := context.WithoutCancel(ctx)
	if _, umountErr := privilegedCommand(cleanup, "umount", "--", dir); umountErr != nil {
		return names, errors.Join(err, fmt.Errorf("%s is still mounted at %s; run sudo umount %s before removing it: %w", drive.Path, dir, dir, umountErr))
	}
	_, _ = privilegedCommand(cleanup, "rmdir", "--", dir)
	return names, err
}

// makeSaveMountPoint has root create a new empty folder below /run and
// checks that the name root reports is one of ours.
func makeSaveMountPoint(ctx context.Context) (string, error) {
	output, err := privilegedCommand(ctx, "mktemp", "-d", saveMountPrefix+"XXXXXX")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(output))
	rest := strings.TrimPrefix(dir, saveMountPrefix)
	if rest == dir || rest == "" || strings.ContainsAny(rest, "/\n") || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("mount folder %q is not below %s", dir, saveMountPrefix)
	}
	return dir, nil
}

func writeFiles(dest installreport.Destination, files []savedFile) ([]string, error) {
	var names []string
	for _, file := range files {
		if _, err := installreport.WriteNew(dest, file.name, file.data); err != nil {
			return names, err
		}
		names = append(names, file.name)
	}
	return names, nil
}
