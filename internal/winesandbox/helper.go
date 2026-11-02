package winesandbox

import "path/filepath"

// The Wine commands live in their own program, gjallar-wine, which the Wine
// app (apps/wine) brings along with Wine and the sandbox's tools. Users
// reach it only through gjallarctl wine (Plane 277), so gjallarctl itself
// carries no Wine. The program sits in libexec of the user's own profile,
// which only root writes: home-manager installs it there.

// HelperPath is where user's gjallar-wine is while Wine apps are on.
func HelperPath(user string) string {
	return filepath.Join("/etc/profiles/per-user", user, "libexec", "gjallar-wine")
}

// CommandsUsage lists the commands gjallar-wine runs for gjallarctl wine.
const CommandsUsage = `       gjallarctl wine add NAME [INSTALLER]  add an app with ~/WineApps/NAME as its
                                             files folder, and run its installer
       gjallarctl wine install ID INSTALLER  run an installer in the app ID
       gjallarctl wine program ID [CHOICE]   show or pick the program the app starts
       gjallarctl wine run ID [ARGS...]      start the app ID in its sandbox
       gjallarctl wine remove ID [--delete-files]
                                             remove the app ID; its files folder
                                             stays unless --delete-files is given
       gjallarctl wine reset ID              give the app ID a new Wine setup;
                                             installed programs go, files stay
       gjallarctl wine repair ID             set the app ID's Wine setup up again;
                                             installed programs stay
       gjallarctl wine undo ID               put back the app ID as it was before
                                             the last reset, repair or fix
       gjallarctl wine doctor ID             tell where Wine finds each library the
                                             app ID's program needs, and what brings
                                             a missing one
       gjallarctl wine check INSTALLER       tell what an installer or program is
                                             built for and needs, without running it
       gjallarctl wine fixes [NAME|LIBRARY]  list the fixes for libraries Wine does
                                             not bring, or tell one, or the fixes
                                             that bring a library such as msvcp100
       gjallarctl wine fix ID FIX            put the fix FIX into the app ID; its
                                             files are checked before they are used
       gjallarctl wine import NAME PREFIX    make a copy of a Wine prefix set up
                                             elsewhere into the app NAME
       gjallarctl wine menu                  bring the app menu in line with the
                                             apps; runs after each rebuild
ID is the app's id, as gjallarctl wine list shows it. CHOICE is a number from
the list or a program's path on C: or D:, such as D:\Tool\tool.exe. An
installer is an .exe or .msi file; add and install check it the same way
before they run it.`
