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
    # A sync root claimed by an earlier account made OpenVFS fail to start;
    # nothing listened, so the client synced the bare folder: placeholders
    # read empty and local edits were never uploaded (2026-10-20).
    grep -Fq '_syncResult.status() != SyncResult::SetupError && _vfsReady;' src/gui/folder.cpp
    grep -A10 '_vfsReady = _vfs->mode() != Vfs::OpenVFS;' src/gui/folder.cpp | grep -Fq 'SyncResult::SetupError'
    grep -Fq 'if (isOpenVfsMountPoint(path)) {' "$f"
    ! grep -Fq 'QTimer::singleShot(1s, this, &Vfs::started);' "$f" || grep -B3 'QTimer::singleShot(1s, this, &Vfs::started);' "$f" | grep -Fq '#else'
    # An openvfs left by a killed client kept its mount; the gate took it as
    # ours, the new openvfs exited and its exit aborted the client
    # (2026-10-21). A mount before our process starts is a setup error.
    grep -B10 '_openVfsProcess = new QProcess(this);' "$f" | grep -Fq 'if (isOpenVfsMountPoint(params.filesystemPath)) {'
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
    # New accounts are all placeholders; only folders picked after the
    # wizard download. The pick must be pinned before local discovery, or
    # the OnlineOnly root dehydrates a folder an interrupted sync fetched.
    grep -Fq 'KeepLocalPickList = 100' src/common/syncjournaldb.h
    grep -A3 'correctPlaceholderFiles();' src/gui/folder.cpp | grep -Fq 'applyKeepLocalPicks(false);'
    grep -A8 'void AccountWizardController::openKeepLocalPicker' src/gui/wizard/accountwizardcontroller.cpp \
      | grep -Fq 'KeepLocalDialog'
    grep -Fq '_pinState = PinState::AlwaysLocal;' src/libsync/discovery.cpp
    # Enforced VFS hid "Make always available locally", so folders could
    # not be picked later in Dolphin or settings. Only turning VFS off stays
    # locked.
    ! grep -B2 'MAKE_AVAILABLE_LOCALLY:' src/gui/socketapi/socketapi.cpp | grep -Fq enforceVirtualFilesSyncFolder
    ! grep -A2 'PinState::AlwaysLocal); });' src/gui/accountsettings.cpp | grep -Fq enforceVirtualFilesSyncFolder
    grep -A2 'slotDisableVfsCurrentFolder);' src/gui/accountsettings.cpp | grep -Fq 'setDisabled(Theme::instance()->enforceVirtualFilesSyncFolder());'
    # Folder names stay out of the log.
    grep -Fq '"Keep-local picks: pinned" << pinned << "dropped" << dropped << "pending" << pending.size();' src/gui/folder.cpp
    # A second start forwards its options to the running client. An unknown
    # one ran the exit meant for a fresh start, so sync stopped and the
    # OpenVFS mount went away (2026-10-21).
    grep -A6 'void Application::showHint' src/gui/application.cpp | grep -Fq 'if (_remoteCommand) {'
    grep -Fq 'if (shouldExit && !_remoteCommand) {' src/gui/application.cpp
    grep -A12 'void Application::parseOptionsRemoteCommand' src/gui/application.cpp | grep -Fq 'if (_remoteOptionsInvalid) {'
    # "Classic sync" stood above virtual-file folders (2026-10-21).
    grep -A40 'void AccountSettings::updateSyncFoldersPanelVisibility' src/gui/accountsettings.cpp \
      | grep -Fq '_ui->syncFoldersPanelTitle->setText(tr("Sync folders"));'
    # A folder's own attributes always said hydrated, so a folder of
    # placeholders got the green tick in Dolphin (book12, 2026-10-13).
    grep -A8 'Vfs::AvailabilityResult OpenVFS::availability' "$f" \
      | grep -Fq 'return availabilityInDb(folderPath);'
    grep -A14 'QString openVfsAvailabilitySocketSuffix' src/gui/socketapi/socketapi.cpp \
      | grep -Fq 'AvailabilityRecursivity::RecursiveAvailability'
    ! grep -Fq 'VFS_HYDRATING' src/gui/socketapi/socketapi.cpp
    touch $out
  '';
}
