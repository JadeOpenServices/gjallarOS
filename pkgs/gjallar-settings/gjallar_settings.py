"""GjallarOS settings window: gjallarHorn and the app catalogue.

Plane 216 (gjallarHorn), 218 (settings window) and 220 (apps). The window
holds no rights of its own: it reads `gjallarctl security status --json`,
`gjallarctl apps list --json` and `gjallarctl wine list --json` (Plane
272), and every change goes through
gjallarctl, which asks the settings service and with it for the password.
"""

import configparser
import json
import os
import re
import signal
import sys

from PySide6.QtCore import Property, QObject, QProcess, QSize, QStandardPaths, QUrl, Signal, Slot
from PySide6.QtGui import QFont, QGuiApplication, QIcon
from PySide6.QtQml import QQmlApplicationEngine
from PySide6.QtQuick import QQuickImageProvider
from PySide6.QtQuickControls2 import QQuickStyle

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

    def _run(self, args, done=None, busy=True):
        process = QProcess(self)
        process.setProgram(GJALLARCTL)
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

    @Property(str, constant=True)
    def downloads(self):
        return QStandardPaths.writableLocation(QStandardPaths.DownloadLocation)

    @Slot()
    def refreshSecurity(self):
        def done(_code, output):
            checks = self._parse(output, "checks")
            self._checks = checks if checks is not None else []
            self.checksChanged.emit()

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
            self.appsChanged.emit()

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

        self._run(["wine", "list", "--json"], done)

    @Slot(str, str, bool)
    def setWine(self, app_id, switch, on):
        """Switch a Wine app's "internet" or "home" network on or off."""
        if not WINE_ID.fullmatch(app_id) or switch not in ("internet", "home"):
            return
        self._run(["wine", "set", app_id, "--" + switch, "on" if on else "off"], lambda *_: self.refreshWine())

    @Slot(str)
    def fix(self, check_id):
        self._run(["security", "fix", check_id], lambda *_: self.refreshSecurity())

    @Slot(str, bool)
    def switchProtection(self, check_id, on):
        self._run(["security", "switch", check_id, "on" if on else "off"], lambda *_: self.refreshSecurity())

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

    @Slot(str, bool)
    def setApp(self, app_id, installed):
        self._run(["apps", "add" if installed else "remove", app_id, "--window"], lambda *_: self.refreshApps())


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
    if start not in ("", "security", "apps"):
        print("Usage: gjallar-settings [security|apps]", file=sys.stderr)
        return 2

    backend = Backend()
    engine = QQmlApplicationEngine()
    engine.addImageProvider("appicon", AppIconProvider(backend))
    engine.setInitialProperties({"backend": backend, "startPage": start, "colours": theme_colours()})
    engine.load(os.path.join(QML_DIR, "Main.qml"))
    if not engine.rootObjects():
        return 1
    code = app.exec()
    # The window goes before the backend it reads from.
    del engine
    return code


if __name__ == "__main__":
    sys.exit(main())
