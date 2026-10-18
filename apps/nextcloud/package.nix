# The OpenVFS Nextcloud client. home.nix and the flake package
# nextcloud-client-openvfs both import this file with the system pkgs, so the
# CI cache builds the same derivation that machines install.
{ pkgs }:
let
  dolphinNextcloudIcons = import ../../themes/apps/dolphin/nextcloud-icons.nix;

  openvfs = pkgs.stdenv.mkDerivation {
    pname = "openvfs";
    version = "0.1.0";

    # Qt6 is present only so ECM can query qtpaths during configuration.
    # OpenVFS itself does not require Qt application wrapping.
    dontWrapQtApps = true;

    src = pkgs.fetchFromGitHub {
      owner = "opencloud-eu";
      repo = "openvfs";
      rev = "2fdf61c8a7c1bb57d78b618ac17eaed12208db7d";
      sha256 = "1nh1pbdb96zyyjsbjf2hfg5whxjdzf5w67yygb2z8rdzzj0chvq4";
    };

    # Bound hydration waits, deliver cancellation, and recognize Nix-wrapped KIO.
    # Release waiters on shutdown or when the sync client goes away.
    patches = [
      ./patches/openvfs-hydration-liveness.patch
      ./patches/openvfs-shutdown.patch
    ];

    nativeBuildInputs = [
      pkgs.cmake
      pkgs.kdePackages.extra-cmake-modules
      pkgs.kdePackages.qtbase
      pkgs.pkg-config
    ];

    buildInputs = [
      pkgs.fuse3
      pkgs.nlohmann_json
    ];
  };

  clientSource = import ./client-source.nix { inherit (pkgs) fetchFromGitHub; };

  client = pkgs.nextcloud-client.overrideAttrs (old: {
    version = "34.0.50-openvfs";

    inherit (clientSource) src patches;

    buildInputs = (old.buildInputs or [ ]) ++ [
      openvfs
      pkgs.kdsingleapplication
    ];

    cmakeFlags = (old.cmakeFlags or [ ]) ++ [
      "-DENFORCE_SINGLE_ACCOUNT=ON"
      # No classic-sync choice, and a classic folder becomes virtual files
      # on the next client start.
      "-DENFORCE_VIRTUAL_FILES_SYNC_FOLDER=ON"
    ];

    postPatch = (old.postPatch or "") + ''
      if ! grep -Fq '"--overrideserverurl"' src/gui/application.cpp \
        || ! grep -Fq '"--overridelocaldir"' src/gui/application.cpp
      then
        echo "Nextcloud interactive wizard override CLI changed" >&2
        exit 1
      fi

      if ! grep -Eq 'option\([[:space:]]*ENFORCE_SINGLE_ACCOUNT' NEXTCLOUD.cmake; then
        echo "Nextcloud no longer exposes ENFORCE_SINGLE_ACCOUNT" >&2
        exit 1
      fi

      account_settings='src/gui/accountsettings.cpp'
      vfs_mode_old='if (folderWizard->property("useVirtualFiles").toBool()) {
        definition.virtualFilesMode = bestAvailableVfsMode();
    }'
      vfs_mode_new='if (ConfigFile().isVfsEnabled() || folderWizard->property("useVirtualFiles").toBool()) {
        definition.virtualFilesMode = bestAvailableVfsMode();
    }'

      vfs_wizard_old='if (definition.virtualFilesMode != Vfs::Off && folderWizard->property("useVirtualFiles").toBool()) {'
      vfs_wizard_new='if (definition.virtualFilesMode != Vfs::Off && (ConfigFile().isVfsEnabled() || folderWizard->property("useVirtualFiles").toBool())) {'

      if [ ! -f "$account_settings" ]; then
        echo "Nextcloud account settings source moved" >&2
        exit 1
      fi

      if grep -Fq "$vfs_mode_old" "$account_settings"; then
        substituteInPlace "$account_settings" \
          --replace-fail \
            "$vfs_mode_old" \
            "$vfs_mode_new"
      elif ! grep -Fq "$vfs_mode_new" "$account_settings"; then
        echo "Nextcloud VFS mode-selection policy changed" >&2
        exit 1
      fi

      if grep -Fq "$vfs_wizard_old" "$account_settings"; then
        substituteInPlace "$account_settings" \
          --replace-fail \
            "$vfs_wizard_old" \
            "$vfs_wizard_new"
      elif ! grep -Fq "$vfs_wizard_new" "$account_settings"; then
        echo "Nextcloud VFS folder-wizard policy changed" >&2
        exit 1
      fi

      echo "GjallarOS OpenVFS folder-wizard policy enforced"

      application='src/gui/application.cpp'
      autostart_enable='Utility::setLaunchOnStartup(_theme->appName(), _theme->appNameGUI(), true);'
      autostart_disable='Utility::setLaunchOnStartup(_theme->appName(), _theme->appNameGUI(), false);'

      if [ ! -f "$application" ]; then
        echo "Nextcloud application startup policy source moved" >&2
        exit 1
      fi

      if grep -Fq "$autostart_enable" "$application"; then
        substituteInPlace "$application" \
          --replace-fail \
            "$autostart_enable" \
            "$autostart_disable"
      elif ! grep -Fq "$autostart_disable" "$application"; then
        echo "Nextcloud wizard autostart policy changed" >&2
        exit 1
      fi

      echo "upstream Nextcloud autostart disabled"

      browser_utility='src/gui/guiutility.cpp'
      if [ ! -f "$browser_utility" ] \
        || ! grep -Fq 'bool Utility::openBrowser(const QUrl &url' "$browser_utility"
      then
        echo "Nextcloud shared browser-opening API changed" >&2
        exit 1
      fi

      if ! grep -Fq '#include <QProcess>' "$browser_utility"; then
        substituteInPlace "$browser_utility" \
          --replace-fail \
            '#include <QDesktopServices>' \
            '#include <QDesktopServices>
#include <QProcess>'
      fi

      if grep -Fq 'if (!QDesktopServices::openUrl(url)) {' "$browser_utility"; then
        substituteInPlace "$browser_utility" \
          --replace-fail \
            'if (!QDesktopServices::openUrl(url)) {' \
            'const auto nextcloudBrowserOpener = qEnvironmentVariable("NEXTCLOUD_BROWSER_OPENER");
    if (!nextcloudBrowserOpener.isEmpty()) {
        const QStringList nextcloudBrowserArguments{url.toString(QUrl::FullyEncoded)};
        if (QProcess::startDetached(nextcloudBrowserOpener, nextcloudBrowserArguments)) {
            return true;
        }
        qCWarning(lcUtility) << "Nextcloud browser opener failed for" << url;
        return false;
    }

    if (!QDesktopServices::openUrl(url)) {'
      elif ! grep -Fq 'NEXTCLOUD_BROWSER_OPENER' "$browser_utility"; then
        echo "Nextcloud browser-opening implementation changed" >&2
        exit 1
      fi

      flow2=""
      for candidate in src/gui/creds/flow2auth.cpp src/gui/flow2auth.cpp; do
        if [ -f "$candidate" ]; then
          flow2="$candidate"
          break
        fi
      done

      if [ -z "$flow2" ]; then
        echo "Nextcloud Flow2 authentication source moved" >&2
        exit 1
      fi

      if grep -Fq 'QDesktopServices::openUrl(' "$flow2"; then
        if ! grep -Fq '#include "guiutility.h"' "$flow2"; then
          sed -i '0,/^#include /s//#include "guiutility.h"\n&/' "$flow2"
        fi
        substituteInPlace "$flow2" \
          --replace-fail \
            'QDesktopServices::openUrl(' \
            'Utility::openBrowser('
      elif ! grep -Fq 'Utility::openBrowser(' "$flow2"; then
        echo "unknown Nextcloud Flow2 browser-opening path" >&2
        exit 1
      fi

      if ! grep -Fq '#include <QProcess>' "$flow2"; then
        sed -i '0,/^#include /s//#include <QProcess>\n&/' "$flow2"
      fi

      if grep -Fq 'emit result(LoggedIn, QString(), loginName, appPassword);' "$flow2"; then
        substituteInPlace "$flow2" \
          --replace-fail \
            'emit result(LoggedIn, QString(), loginName, appPassword);' \
            'emit result(LoggedIn, QString(), loginName, appPassword);
    const auto nextcloudBrowserCloser = qEnvironmentVariable("NEXTCLOUD_BROWSER_CLOSER");
    if (!nextcloudBrowserCloser.isEmpty()) {
        QProcess::startDetached(nextcloudBrowserCloser, {});
    }'
      elif grep -Fq 'Q_EMIT result(LoggedIn, QString(), loginName, appPassword);' "$flow2"; then
        substituteInPlace "$flow2" \
          --replace-fail \
            'Q_EMIT result(LoggedIn, QString(), loginName, appPassword);' \
            'Q_EMIT result(LoggedIn, QString(), loginName, appPassword);
    const auto nextcloudBrowserCloser = qEnvironmentVariable("NEXTCLOUD_BROWSER_CLOSER");
    if (!nextcloudBrowserCloser.isEmpty()) {
        QProcess::startDetached(nextcloudBrowserCloser, {});
    }'
      elif ! grep -Fq 'NEXTCLOUD_BROWSER_CLOSER' "$flow2"; then
        echo "unknown Nextcloud Flow2 success path" >&2
        exit 1
      fi

      socket_api='src/gui/socketapi/socketapi.cpp'
      dolphin_overlay='shell_integration/dolphin/ownclouddolphinoverlayplugin.cpp'

      if [ ! -f "$socket_api" ] || [ ! -f "$dolphin_overlay" ]; then
        echo "Nextcloud Dolphin/OpenVFS shell integration source moved" >&2
        exit 1
      fi

      availability_helper='void setClipboardText(const QString &text)'
      availability_helper_new='QString openVfsAvailabilitySocketSuffix(OCC::Folder *folder, const QString &relativePath)
{
    if (!folder || folder->vfs().mode() != OCC::Vfs::OpenVFS) {
        return {};
    }

    const auto availability = folder->vfs().availability(
        relativePath,
        OCC::Vfs::AvailabilityRecursivity::NotRecursiveAvailability);

    if (!availability) {
        return {};
    }

    switch (*availability) {
    case OCC::VfsItemAvailability::AlwaysLocal:
    case OCC::VfsItemAvailability::AllHydrated:
        return QStringLiteral("+VFS_LOCAL");
    case OCC::VfsItemAvailability::AllDehydrated:
    case OCC::VfsItemAvailability::OnlineOnly:
        return QStringLiteral("+VFS_ONLINE_ONLY");
    case OCC::VfsItemAvailability::Mixed:
        return QStringLiteral("+VFS_HYDRATING");
    }

    return {};
}

void setClipboardText(const QString &text)'

      if grep -Fq "$availability_helper" "$socket_api" \
        && ! grep -Fq 'openVfsAvailabilitySocketSuffix' "$socket_api"
      then
        substituteInPlace "$socket_api" \
          --replace-fail \
            "$availability_helper" \
            "$availability_helper_new"
      elif ! grep -Fq 'openVfsAvailabilitySocketSuffix' "$socket_api"; then
        echo "Nextcloud socket availability helper anchor changed" >&2
        exit 1
      fi

      broadcast_old='    QString msg = buildMessage(QLatin1String("STATUS"), systemPath, fileStatus.toSocketAPIString());'
      broadcast_new='    const auto fileData = FileData::get(systemPath);
    const QString statusString = fileStatus.toSocketAPIString()
        + openVfsAvailabilitySocketSuffix(fileData.folder, fileData.folderRelativePath);
    QString msg = buildMessage(QLatin1String("STATUS"), systemPath, statusString);'

      if grep -Fq "$broadcast_old" "$socket_api"; then
        substituteInPlace "$socket_api" \
          --replace-fail \
            "$broadcast_old" \
            "$broadcast_new"
      elif ! grep -Fq 'openVfsAvailabilitySocketSuffix(fileData.folder, fileData.folderRelativePath)' "$socket_api"; then
        echo "Nextcloud socket status-broadcast implementation changed" >&2
        exit 1
      fi

      retrieve_old='        SyncFileStatus fileStatus = fileData.syncFileStatus();
        statusString = fileStatus.toSocketAPIString();'
      retrieve_new='        SyncFileStatus fileStatus = fileData.syncFileStatus();
        statusString = fileStatus.toSocketAPIString()
            + openVfsAvailabilitySocketSuffix(fileData.folder, fileData.folderRelativePath);'

      if grep -Fq "$retrieve_old" "$socket_api"; then
        substituteInPlace "$socket_api" \
          --replace-fail \
            "$retrieve_old" \
            "$retrieve_new"
      elif ! grep -Fq 'statusString = fileStatus.toSocketAPIString()' "$socket_api" \
        || ! grep -Fq 'openVfsAvailabilitySocketSuffix(fileData.folder, fileData.folderRelativePath)' "$socket_api"
      then
        echo "Nextcloud socket status-retrieval implementation changed" >&2
        exit 1
      fi

      overlay_old='        if (status.startsWith("OK")) {
            r << QStringLiteral("vcs-normal");
        }'
      overlay_new='        if (status.startsWith("OK")) {
            if (status.contains("+VFS_ONLINE_ONLY")) {
                r << QStringLiteral("${dolphinNextcloudIcons.onlineOnly}");
            } else if (status.contains("+VFS_HYDRATING")) {
                r << QStringLiteral("vcs-update-required");
            } else {
                // Any other OK state is fully synced and locally available.
                r << QStringLiteral("${dolphinNextcloudIcons.local}");
            }
        }'

      if grep -Fq "$overlay_old" "$dolphin_overlay"; then
        substituteInPlace "$dolphin_overlay" \
          --replace-fail \
            "$overlay_old" \
            "$overlay_new"
      elif ! grep -Fq '+VFS_ONLINE_ONLY' "$dolphin_overlay"; then
        echo "Nextcloud Dolphin overlay mapping changed" >&2
        exit 1
      fi

      echo "Nextcloud Dolphin OpenVFS availability overlays secured"

      echo "Nextcloud Flow2 auth-window close hook secured"
      echo "Nextcloud enrollment browser hook secured"
    '';

    postInstall = (old.postInstall or "") + ''
      vfs_plugin="$(${pkgs.findutils}/bin/find "$out" \
        \( -type f -o -type l \) \
        -name 'nextcloudsync_vfs_*.so' \
        -print -quit)"

      if [ -z "$vfs_plugin" ]; then
        echo "Nextcloud build has no Linux VFS plugin; refusing classic-sync fallback" >&2
        exit 1
      fi

      echo "Nextcloud Linux VFS plugin: $vfs_plugin"
    '';
  });
in
{
  inherit openvfs client;
}
