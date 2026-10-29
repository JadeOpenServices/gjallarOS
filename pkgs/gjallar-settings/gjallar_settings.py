"""GjallarOS settings window: gjallarHorn and the app catalogue.

Plane 216 (gjallarHorn), 218 (settings window) and 220 (apps). The window
holds no rights of its own: it reads `gjallarctl security status --json`
and `gjallarctl apps list --json`, and every change goes through
gjallarctl, which asks the settings service and with it for the password.
"""

import configparser
import json
import os
import signal
import sys

from PySide6.QtCore import Property, QObject, QProcess, QStandardPaths, QUrl, Signal, Slot
from PySide6.QtGui import QFont, QGuiApplication, QIcon
from PySide6.QtQml import QQmlApplicationEngine
from PySide6.QtQuickControls2 import QQuickStyle

GJALLARCTL = "@gjallarctl@"
QML_DIR = "@qml@"

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


class Backend(QObject):
    checksChanged = Signal()
    appsChanged = Signal()
    busyChanged = Signal()
    scanChanged = Signal()

    def __init__(self):
        super().__init__()
        self._checks = []
        self._apps = []
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
            self.appsChanged.emit()

        self._run(["apps", "list", "--json"], done)

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
