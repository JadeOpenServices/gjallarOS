{ pkgs }:
let
  client = import ../../apps/nextcloud/client-source.nix { inherit (pkgs) fetchFromGitHub replaceVars; };
in
pkgs.stdenvNoCC.mkDerivation {
  name = "gjallar-nextcloud-client-patches-check";
  inherit (client) src patches;
  dontConfigure = true;
  dontBuild = true;
  # OpenVFS files carry an "Inherited" pin. Checking only a file's own pin
  # kept every song copied out of an OnlineOnly folder downloaded for good
  # (2026-09-30); discovery must use the effective pin like xattr/suffix VFS.
  installPhase = ''
    f=src/libsync/vfs/openvfs/vfs_openvfs.cpp
    grep -Fq 'if (pin && *pin == PinState::OnlineOnly) {' "$f"
    grep -Fq 'if (pin && *pin == PinState::AlwaysLocal) {' "$f"
    # New local files have no placeholder attributes (default "Hydrated");
    # they must never be classified for dehydration.
    grep -Fq '} else if (attribs && attribs.state == ::OpenVFS::Constants::States::Hydrated) {' "$f"
    ! grep -Fq 'attribs.pinState == convertPinState(PinState::OnlineOnly)' "$f"
    # Reporting placeholders out of sync re-ran a no-op job on ~18k unchanged
    # files every sync run, a full core for hours (2026-10-05).
    grep -A6 'bool OpenVFS::isPlaceHolderInSync' "$f" | grep -Fq 'return true;'
    # A conflict file flipped between Excluded and Inherited pins, each xattr
    # write woke the watcher, and the client synced every 5 s; hydrated files
    # got the download time as mtime and were uploaded again (2026-10-09).
    grep -A12 'void OpenVFS::fileStatusChanged' "$f" | grep -Fq 'Utility::isConflictFile(systemFileName)'
    grep -A12 'bool OpenVFS::setPinState' "$f" | grep -Fq 'if (attribs.pinState == convertPinState(state)) {'
    grep -B8 'Q_EMIT finished();' src/libsync/hydrationjob.cpp | grep -Fq '_device->close();'
    # Upstream defaults Linux to classic sync; with the preset server and
    # folder the wizard never asks, and new accounts downloaded everything
    # (2026-10-09).
    ! grep -Fq '#ifndef Q_OS_LINUX' src/gui/wizard/accountwizardcontroller.cpp
    # QML windows used fixed colors keyed off the color scheme while Kvantum
    # drew controls from the Noctalia palette: white windows with light text
    # and dark buttons (2026-10-10). Colors and dark mode come from the palette.
    grep -A8 'bool Theme::darkMode() const' src/libsync/theme.cpp \
      | grep -Fq 'isDarkColor(QGuiApplication::palette().window().color())'
    grep -A16 'void Theme::systemPaletteHasChanged' src/libsync/theme.cpp \
      | grep -Fq 'Q_EMIT darkModeChanged();'
    grep -Fq 'readonly property color wizardWindowBackground: pal.window' theme/Style/Style.qml
    # Only warning and error colors stay fixed.
    ! grep -E 'property color (wizard|listItemHover)[A-Za-z]*: .*"#' theme/Style/Style.qml \
      | grep -Ev 'wizard(Warning|Error)'
    # The login-window opener rejects anything but https. A string edit also
    # hooked the mail composer, so mail links did nothing (2026-10-19).
    [ "$(grep -c NEXTCLOUD_BROWSER_OPENER src/gui/guiutility.cpp)" = 1 ]
    grep -A25 'bool Utility::openBrowser' src/gui/guiutility.cpp | grep -Fq NEXTCLOUD_BROWSER_OPENER
    # The session starts the client. The setting defaulted to on, so the
    # first start still wrote ~/.config/autostart (2026-10-20).
    grep -Fq 'settings.value(launchOnSystemStartupC, false)' src/libsync/configfile.cpp
    ! grep -Fq 'appNameGUI(), true);' src/gui/application.cpp
    touch $out
  '';
}
