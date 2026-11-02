"""GjallarOS settings window: gjallarHorn and the app catalogue.

Plane 216 (gjallarHorn), 218 (settings window) and 220 (apps). The window
holds no rights of its own: it reads `gjallarctl security status --json`,
`gjallarctl apps list --json` and `gjallarctl wine list --json` (Plane
272), and every change goes through
gjallarctl, which asks the settings service and with it for the password.
A click only marks a change; Apply runs them all at once (Plane 276).
New Wine apps are set up with gjallar-wine in a terminal, as the user.
"""

import configparser
import json
import os
import re
import shutil
import signal
import sys

from PySide6.QtCore import Property, QObject, QProcess, QSize, QStandardPaths, QUrl, Signal, Slot
from PySide6.QtGui import QDesktopServices, QFont, QGuiApplication, QIcon
from PySide6.QtQml import QQmlApplicationEngine
from PySide6.QtQuick import QQuickImageProvider
from PySide6.QtQuickControls2 import QQuickStyle

DETAILS = "@details@"
GJALLARCTL = "@gjallarctl@"
ICON_DIR = "@icons@"
QML_DIR = "@qml@"

# Freedesktop category icons for apps the icon theme has no icon for.
CATEGORY_ICONS = {
    "communication": "applications-internet",
    "compatibility": "applications-other",
    "development": "applications-development",
    "files": "system-file-manager",
    "graphics": "applications-graphics",
    "media": "applications-multimedia",
    "network": "applications-internet",
    "office": "applications-office",
    "security": "security-high",
    "system": "applications-system",
}
APP_ID = re.compile(r"[a-z0-9][a-z0-9-]*")
# Wine app ids as gjallar-wine makes them.
WINE_ID = re.compile(r"[a-z][a-z0-9-]*")
# Setup and program choice run in a terminal, as gjallarctl's own --window
# commands do: the output and questions stay readable until Enter.
TERMINAL_SCRIPT = """"$@"; printf '\\nPress Enter to close. '; read -r _"""
# What a marked change can be: an app installed, a Wine app's network, a
# gjallarHorn protection. Each is "kind:id" in Backend.changes.
CHANGE_KINDS = {"app": APP_ID, "internet": WINE_ID, "home": WINE_ID, "security": APP_ID}
# Homepages the detail pages may open: https, a host, a plain path.
HOMEPAGE = re.compile(r"https://[A-Za-z0-9.-]+(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~/%+-]*)?")

# Obsidian (themes/palettes/openjade.nix) for the colours Qt's palette has
# no role for, until Noctalia has written its colour scheme.
FALLBACK_COLOURS = {"positive": "#4FA89D", "neutral": "#E3B464", "negative": "#E5484D"}


def theme_colours():
    """State colours from the Noctalia KDE colour scheme (user/wm/shells/noctalia/qt.nix)."""
    colours = dict(FALLBACK_COLOURS)
    data = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    scheme = configparser.ConfigParser(interpolation=None)
    try:
        scheme.read(os.path.join(data, "color-schemes", "Noctalia.colors"))
        window = scheme["Colors:Window"]
    except (configparser.Error, KeyError):
        return colours
    # The scheme maps primary to DecorationFocus, secondary to
    # ForegroundNeutral and error to ForegroundNegative.
    for name, key in (("positive", "DecorationFocus"), ("neutral", "ForegroundNeutral"), ("negative", "ForegroundNegative")):
        value = window.get(key, "").strip()
        if len(value) == 7 and value.startswith("#"):
            colours[name] = value
    return colours


def load_details(path):
    """Licence, homepage and system flag per app, built from package meta (default.nix)."""
    try:
        with open(path, encoding="utf-8") as file:
            data = json.load(file)
    except (OSError, ValueError):
        return {}
    if not isinstance(data, dict):
        return {}
    details = {}
    for app_id, entry in data.items():
        if not APP_ID.fullmatch(app_id) or not isinstance(entry, dict):
            continue
        licences = entry.get("licences")
        homepage = entry.get("homepage")
        details[app_id] = {
            "licences": [l for l in licences if isinstance(l, str)] if isinstance(licences, list) else [],
            "homepage": homepage if isinstance(homepage, str) and HOMEPAGE.fullmatch(homepage) else "",
            "system": entry.get("system") is True,
        }
    return details


class AppIconProvider(QQuickImageProvider):
    """image://appicon/<id>: the repo icon, else the theme icon, else the category icon (Plane 273)."""

    def __init__(self, backend):
        super().__init__(QQuickImageProvider.ImageType.Pixmap)
        self._backend = backend

    def requestPixmap(self, app_id, size, requested):
        side = max(requested.width(), requested.height(), 32)
        size.setWidth(side)
        size.setHeight(side)
        icon = QIcon()
        if APP_ID.fullmatch(app_id):
            path = os.path.join(ICON_DIR, app_id + ".svg")
            if os.path.isfile(path):
                icon = QIcon(path)
            app = next((a for a in self._backend.apps if isinstance(a, dict) and a.get("id") == app_id), {})
            if icon.isNull():
                icon = QIcon.fromTheme(app.get("icon") or app_id)
            if icon.isNull():
                icon = QIcon.fromTheme(CATEGORY_ICONS.get(app.get("category"), "applications-other"))
        if icon.isNull():
            icon = QIcon.fromTheme("applications-other")
        return icon.pixmap(QSize(side, side))


class Backend(QObject):
    checksChanged = Signal()
    appsChanged = Signal()
    wineChanged = Signal()
    busyChanged = Signal()
    scanChanged = Signal()
    changesChanged = Signal()
    noticeChanged = Signal()

    def __init__(self):
        super().__init__()
        self._checks = []
        self._apps = []
        self._appsSettings = ""
        self._appsRebuilding = False
        self._wine = []
        self._wineSettings = ""
        self._busy = 0
        self._running = set()
        self._scan = {}
        self._scanning = ""
        self._wineTool = ""
        self._changes = {}
        self._applying = False
        self._details = load_details(DETAILS)
        self._notice = ""

    def _run(self, args, done=None, busy=True, program=GJALLARCTL, merged=False):
        process = QProcess(self)
        process.setProgram(program)
        if merged:
            process.setProcessChannelMode(QProcess.ProcessChannelMode.MergedChannels)
        process.setArguments(args)
        self._running.add(process)
        if busy:
            self._busy += 1
            self.busyChanged.emit()

        def finished(code, _status):
            output = bytes(process.readAllStandardOutput()).decode(errors="replace")
            self._running.discard(process)
            if busy:
                self._busy -= 1
                self.busyChanged.emit()
            process.deleteLater()
            if done:
                done(code, output)

        process.finished.connect(finished)
        # A program that cannot start never sends finished.
        process.errorOccurred.connect(
            lambda error: finished(-1, None) if error == QProcess.ProcessError.FailedToStart else None)
        process.start()

    @staticmethod
    def _parse(output, field):
        try:
            value = json.loads(output)[field]
        except (ValueError, KeyError, TypeError):
            return None
        return value if isinstance(value, list) else None

    @Property("QVariantList", notify=checksChanged)
    def checks(self):
        return self._checks

    @Property("QVariantList", notify=appsChanged)
    def apps(self):
        return self._apps

    # "ok", "missing" (no settings service: the system still builds from
    # the user's own checkout) or "failed".
    @Property(str, notify=appsChanged)
    def appsSettings(self):
        return self._appsSettings

    @Property(bool, notify=appsChanged)
    def appsRebuilding(self):
        return self._appsRebuilding

    @Property("QVariantList", notify=wineChanged)
    def wineApps(self):
        return self._wine

    # As appsSettings, for the Wine apps' network switches.
    @Property(str, notify=wineChanged)
    def wineSettings(self):
        return self._wineSettings

    @Property(bool, notify=busyChanged)
    def busy(self):
        return self._busy > 0

    @Property("QVariantMap", notify=scanChanged)
    def scanResult(self):
        return self._scan

    @Property(str, notify=scanChanged)
    def scanning(self):
        return self._scanning

    # gjallar-wine comes with the Wine app (apps/wine); without it there is
    # nothing to set up.
    @Property(bool, notify=appsChanged)
    def wineTool(self):
        return self._wineTool != ""

    # Marked changes, "kind:id" -> the state asked for; nothing runs before
    # apply().
    @Property("QVariantMap", notify=changesChanged)
    def changes(self):
        return self._changes

    @Property(int, notify=changesChanged)
    def changeCount(self):
        return len(self._changes)

    @Property(bool, notify=changesChanged)
    def applying(self):
        return self._applying

    @Property("QVariantMap", constant=True)
    def appDetails(self):
        return self._details

    @Property(str, notify=noticeChanged)
    def notice(self):
        return self._notice

    @Slot()
    def clearNotice(self):
        self._notice = ""
        self.noticeChanged.emit()

    @Property(str, constant=True)
    def downloads(self):
        return QStandardPaths.writableLocation(QStandardPaths.DownloadLocation)

    @Slot()
    def refreshSecurity(self):
        def done(_code, output):
            checks = self._parse(output, "checks")
            self._checks = checks if checks is not None else []
            self.checksChanged.emit()
            self._prune()

        self._run(["security", "status", "--json"], done)

    @Slot()
    def refreshApps(self):
        def done(_code, output):
            entries = self._parse(output, "apps")
            self._apps = entries if entries is not None else []
            try:
                answer = json.loads(output)
            except ValueError:
                answer = None
            if not isinstance(answer, dict):
                answer = {}
            state = answer.get("settings", "")
            self._appsSettings = state if isinstance(state, str) else ""
            self._appsRebuilding = answer.get("rebuilding") is True
            # Turning Wine on or off in this window adds or removes it.
            self._wineTool = shutil.which("gjallar-wine") or ""
            self.appsChanged.emit()
            self._prune()

        self._run(["apps", "list", "--json"], done)

    @Slot()
    def refreshWine(self):
        def done(_code, output):
            entries = self._parse(output, "apps")
            self._wine = [e for e in entries or [] if isinstance(e, dict)]
            try:
                state = json.loads(output).get("settings", "")
            except (ValueError, AttributeError):
                state = ""
            self._wineSettings = state if isinstance(state, str) else ""
            self.wineChanged.emit()
            self._prune()

        self._run(["wine", "list", "--json"], done)

    def _wineTerminal(self, title, args):
        self._run(["--title=" + title, "-e", "sh", "-c", TERMINAL_SCRIPT, "sh", self._wineTool, *args],
                  lambda *_: self.refreshWine(), program="ghostty")

    @Slot(str, str)
    def setUpWine(self, name, installer):
        """Add a Wine app called name and run its installer, a file:// URL from the file dialog."""
        name = name.strip()
        if installer.startswith("file:"):
            installer = QUrl(installer).toLocalFile()
        # gjallar-wine checks both again; these only keep a wrong pick out.
        if not self._wineTool or not name or not os.path.isabs(installer) or not os.path.isfile(installer):
            return
        self._wineTerminal("GjallarOS Wine app", ["add", name, installer])

    @Slot(str)
    def pickWineProgram(self, app_id):
        """Choose the program a Wine app starts, from the programs its shortcuts start."""
        if self._wineTool and WINE_ID.fullmatch(app_id):
            self._wineTerminal("GjallarOS Wine app", ["program", app_id])

    @Slot(str, bool)
    def removeWine(self, app_id, delete_files):
        """Remove a Wine app; its files folder stays unless delete_files."""
        if not self._wineTool or not WINE_ID.fullmatch(app_id) or self._applying:
            return

        def done(_code, output):
            lines = (output or "").strip().splitlines()
            self._notice = lines[-1][:300] if lines else "The Wine tool did not start."
            self.noticeChanged.emit()
            self.refreshWine()

        self._run(["remove", app_id, *(["--delete-files"] if delete_files else [])], done,
                  program=self._wineTool, merged=True)

    @Slot(str)
    def openHomepage(self, app_id):
        """Open an app's homepage from the built-in details; QML never passes a URL."""
        url = self._details.get(app_id, {}).get("homepage", "")
        if url:
            QDesktopServices.openUrl(QUrl(url, QUrl.ParsingMode.StrictMode))

    @Slot(str)
    def fix(self, check_id):
        self._run(["security", "fix", check_id], lambda *_: self.refreshSecurity())

    @Slot(str)
    def applyWaiting(self, check_id):
        self._run(["security", "apply", check_id], lambda *_: self.refreshSecurity())

    @Slot(str)
    def cancelWaiting(self, check_id):
        self._run(["security", "cancel", check_id], lambda *_: self.refreshSecurity())

    @Slot(str)
    def scan(self, target):
        """Scan a folder; target is a path or a file:// URL from the folder dialog."""
        if target.startswith("file:"):
            target = QUrl(target).toLocalFile()
        if not target or self._scanning:
            return
        self._scanning, self._scan = target, {}
        self.scanChanged.emit()

        def done(_code, output):
            try:
                result = json.loads(output)
            except ValueError:
                result = None
            if not isinstance(result, dict):
                result = {"target": target, "found": [], "error": "The scan did not finish."}
            self._scanning, self._scan = "", result
            self.scanChanged.emit()

        # A scan can take minutes; it does not hold the rest of the page.
        self._run(["security", "scan", target, "--json"], done, busy=False)

    def _current(self, kind, item_id):
        """The state a change is measured against, or None when it is not known."""
        if kind == "app":
            entry = next((a for a in self._apps if isinstance(a, dict) and a.get("id") == item_id), {})
            value = entry.get("installed")
        elif kind in ("internet", "home"):
            entry = next((w for w in self._wine if w.get("id") == item_id), {})
            value = entry.get(kind) is True if entry else None
        else:
            entry = next((c for c in self._checks if isinstance(c, dict) and c.get("id") == item_id), {})
            # A change already waiting has its own card.
            value = None if entry.get("waiting") else {"on": True, "off": False}.get(entry.get("switch"))
        return value if isinstance(value, bool) else None

    def _prune(self):
        """Drop marks that the system now matches or that can no longer apply."""
        if self._applying:
            return
        kept = {key: value for key, value in self._changes.items()
                if self._current(*key.split(":", 1)) not in (None, value)}
        if kept != self._changes:
            self._changes = kept
            self.changesChanged.emit()

    @Slot(str, str, bool)
    def mark(self, kind, item_id, value):
        """Mark a change; marking the state the system already has takes the mark away."""
        pattern = CHANGE_KINDS.get(kind)
        if self._applying or not pattern or not pattern.fullmatch(item_id):
            return
        current = self._current(kind, item_id)
        if current is None:
            return
        key = kind + ":" + item_id
        changes = dict(self._changes)
        if value == current:
            changes.pop(key, None)
        else:
            changes[key] = value
        self._changes = changes
        self.changesChanged.emit()

    @Slot(str, str)
    def undo(self, kind, item_id):
        key = kind + ":" + item_id
        if not self._applying and key in self._changes:
            changes = dict(self._changes)
            del changes[key]
            self._changes = changes
            self.changesChanged.emit()

    @Slot()
    def discard(self):
        if not self._applying and self._changes:
            self._changes = {}
            self.changesChanged.emit()

    def _steps(self):
        """The commands for the marked changes: all apps in one rebuild, one call per Wine app and protection."""
        apps, wine, security = [], {}, []
        for key, value in sorted(self._changes.items()):
            kind, item_id = key.split(":", 1)
            if kind == "app":
                apps.append(("+" if value else "-") + item_id)
            elif kind in ("internet", "home"):
                wine.setdefault(item_id, []).extend(["--" + kind, "on" if value else "off"])
            else:
                security.append(["security", "switch", item_id, "on" if value else "off"])
        steps = [["apps", "change", *apps, "--window"]] if apps else []
        steps += [["wine", "set", item_id, *flags] for item_id, flags in wine.items()]
        return steps + security

    @Slot()
    def apply(self):
        """Run the marked changes one after the other; a step that fails stops the rest."""
        steps = self._steps()
        if self._applying or not steps:
            return
        self._applying = True
        self.changesChanged.emit()

        def finish():
            self._applying = False
            self.changesChanged.emit()
            self.refreshApps()
            self.refreshWine()
            self.refreshSecurity()

        def next_step(code=0, _output=""):
            if code != 0 or not steps:
                finish()
                return
            self._run(steps.pop(0), next_step)

        next_step()


def wine_installer(arg):
    """The file:// URL of installer arg, a path or a file:// URL, or "" if it is no installer file."""
    path = QUrl(arg).toLocalFile() if arg.startswith("file:") else arg
    if not os.path.isabs(path) or not os.path.isfile(path):
        return ""
    if os.path.splitext(path)[1].lower() not in (".exe", ".msi"):
        return ""
    return QUrl.fromLocalFile(os.path.normpath(path)).toString()


def main():
    # Qt's event loop never hands Ctrl+C to Python, so let it end the window
    # the usual way when started from a terminal.
    signal.signal(signal.SIGINT, signal.SIG_DFL)
    app = QGuiApplication(sys.argv)
    app.setApplicationName("GjallarOS Settings")
    app.setDesktopFileName("gjallar-settings")
    app.setWindowIcon(QIcon.fromTheme("preferences-system"))
    app.setFont(QFont("Fira Sans", 11))
    # Fusion draws with the system palette, which qt6ct takes from the
    # active Noctalia theme.
    QQuickStyle.setStyle("Fusion")

    start = sys.argv[1] if len(sys.argv) > 1 else ""
    installer = ""
    if start == "wine-setup" and len(sys.argv) == 3:
        # A double click on an installer in the file manager (Plane 275).
        installer = wine_installer(sys.argv[2])
        if not installer:
            print("gjallar-settings: wine-setup needs an .exe or .msi file", file=sys.stderr)
            return 2
        start = "apps"
    elif start not in ("", "security", "apps") or len(sys.argv) > 2:
        print("Usage: gjallar-settings [security|apps|wine-setup INSTALLER]", file=sys.stderr)
        return 2

    backend = Backend()
    engine = QQmlApplicationEngine()
    engine.addImageProvider("appicon", AppIconProvider(backend))
    engine.setInitialProperties({"backend": backend, "startPage": start, "wineInstaller": installer,
                                 "colours": theme_colours()})
    engine.load(os.path.join(QML_DIR, "Main.qml"))
    if not engine.rootObjects():
        return 1
    code = app.exec()
    # The window goes before the backend it reads from.
    del engine
    return code


if __name__ == "__main__":
    sys.exit(main())
