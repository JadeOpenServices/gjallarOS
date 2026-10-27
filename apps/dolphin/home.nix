{
  config,
  lib,
  pkgs,
  settings,
  gjallarRun,
  ...
}:
let
  dolphinTheme = import ../../themes/apps/dolphin/darkly.nix { inherit pkgs; };
  dolphinNextcloudIcons = import ../../themes/apps/dolphin/nextcloud-icons.nix;
  # Nextcloud parts only when its client is installed and configured.
  nextcloudActive = config.gjallar.apps.nextcloud.enable && (settings.nextcloudEnable or false);
  dolphinClient =
    if nextcloudActive then
      pkgs.symlinkJoin {
        name = "dolphin-openvfs";
        paths = [ pkgs.kdePackages.dolphin ];
        nativeBuildInputs = [ pkgs.makeWrapper ];
        postBuild = ''
          # In-process KIO file workers look like Dolphin to OpenVFS and are
          # excluded from hydration along with previews. Keep file copies in
          # identifiable workers; thumbnail workers remain excluded.
          rm "$out/bin/dolphin"
          makeWrapper ${lib.getExe pkgs.kdePackages.dolphin} "$out/bin/dolphin" \
            --set KIO_ENABLE_WORKER_THREADS 0
          service="$out/share/dbus-1/services/org.kde.dolphin.FileManager1.service"
          cp --remove-destination "$service" "$service.tmp"
          rm "$service"
          mv "$service.tmp" "$service"
          chmod u+w "$service"
          substituteInPlace "$service" \
            --replace-fail ${lib.escapeShellArg (lib.getExe pkgs.kdePackages.dolphin)} "$out/bin/dolphin"
        '';
        meta.mainProgram = "dolphin";
      }
    else
      pkgs.kdePackages.dolphin;

  fileManager = pkgs.writeShellApplication {
    name = "file-manager";
    text = ''
      if [ "$#" -eq 0 ]; then
        set -- "$HOME"
      fi

      # Darkly is Dolphin-local. qtct remains active and continues supplying
      # the live Noctalia/KColorScheme palette.
      export QT_STYLE_OVERRIDE=${lib.escapeShellArg dolphinTheme.styleName}
      export QT_PLUGIN_PATH=${lib.escapeShellArg dolphinTheme.qtPluginPath}''${QT_PLUGIN_PATH:+:$QT_PLUGIN_PATH}

      exec ${lib.getExe gjallarRun} ${lib.getExe dolphinClient} --new-window "$@"
    '';
  };
  ini = pkgs.formats.ini { };
  defaults = ini.generate "dolphin-defaults" {
    General = {
      AlwaysShowTabBar = true;
      BrowseThroughArchives = true;
      FilterBar = true;
      GlobalViewProps = true;
      OpenExternallyCalledFolderInNewTab = false;
      RememberOpenedTabs = false;
      ShowFullPath = true;
      ShowToolTips = true;
      ShowZoomSlider = true;
      SortingChoice = 0;
    };
    ContextMenu.ShowCopyMoveMenu = true;
    InformationPanel = {
      previewsShown = true;
      previewsAutoPlay = false;
    };
    # Keep remote browsing from fetching file content merely for thumbnails.
    PreviewSettings = {
      EnableRemoteFolderThumbnail = false;
      MaximumRemoteSize = 0;
    };
  };
  editorDesktop =
    {
      vscodium = "codium.desktop";
      vscode = "code.desktop";
    }
    .${settings.preferredEditor} or null;

  # KService lists apps only through the XDG menu. Plasma ships one, a
  # Hyprland session has none, so KDE apps saw no apps at all and Dolphin
  # asked what to open every file with, mimeapps.list or not.
  applicationsMenu = pkgs.writeText "applications.menu" ''
    <!DOCTYPE Menu PUBLIC "-//freedesktop//DTD Menu 1.0//EN"
     "http://www.freedesktop.org/standards/menu-spec/1.0/menu.dtd">
    <Menu>
      <Name>Applications</Name>
      <DefaultAppDirs/>
      <DefaultDirectoryDirs/>
      <DefaultMergeDirs/>
      <Include><All/></Include>
    </Menu>
  '';

  imageTypes = [
    "image/png"
    "image/jpeg"
    "image/gif"
    "image/webp"
    "image/x-webp"
    "image/avif"
    "image/heif"
    "image/jxl"
    "image/bmp"
    "image/tiff"
    "image/svg+xml"
    "image/svg+xml-compressed"
    "image/x-ico"
    "image/x-icns"
    "image/x-tga"
    "image/x-portable-bitmap"
    "image/x-portable-graymap"
    "image/x-portable-pixmap"
    "image/x-xbitmap"
    "image/x-xpixmap"
  ];
in
lib.mkIf config.gjallar.apps.dolphin.enable {
  _module.args.gjallarFileManager = fileManager;

  home.packages = with pkgs.kdePackages; [
    dolphinTheme.package
    dolphinClient
    dolphin-plugins
    ark
    gwenview
    kio-extras
    kio-fuse
    kdegraphics-thumbnailers
    ffmpegthumbs
    konsole
    kfind
    filelight
    fileManager
    (pkgs.writeTextDir "share/zsh/site-functions/_file-manager" ''
      #compdef file-manager
      _arguments '*:folder or file:_files'
    '')
  ];

  # Touchpad: pkgs/overlays/dolphin.nix. Touchscreen: built into Dolphin.
  gjallar.keybindHelp.gestures = [
    {
      keys = "Two fingers right or left in Dolphin";
      description = "Go back or forward";
    }
  ];

  # Stylix supplies the desktop's dark Kvantum palette and fonts.
  qt.qt6ctSettings.Appearance.icon_theme = lib.mkDefault settings.themeDetails.icons;

  # Dolphin-specific Nextcloud availability glyphs. Unique names avoid
  # mutating Papirus or other applications' icon lookup.
  xdg.dataFile."icons/hicolor/scalable/places/${dolphinNextcloudIcons.places}.svg" =
    lib.mkIf nextcloudActive {
      source = dolphinNextcloudIcons.cloudSource;
    };

  xdg.dataFile."icons/hicolor/scalable/emblems/${dolphinNextcloudIcons.onlineOnly}.svg" =
    lib.mkIf nextcloudActive {
      source = dolphinNextcloudIcons.cloudSource;
    };

  xdg.dataFile."icons/hicolor/scalable/emblems/${dolphinNextcloudIcons.local}.svg" =
    lib.mkIf nextcloudActive {
      source = dolphinNextcloudIcons.localSource;
    };

  # Seed once: Dolphin owns its live settings so UI changes survive rebuilds.
  home.activation.dolphinDefaults = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    dolphin_config=${lib.escapeShellArg "${config.xdg.configHome}/dolphinrc"}
    if [ ! -e "$dolphin_config" ] && [ ! -L "$dolphin_config" ]; then
      run ${pkgs.coreutils}/bin/install -D -m 0600 ${defaults} "$dolphin_config"
    fi
  '';

  # Do not restore an ever-growing collection of old tabs, and do
  # not funnel externally opened folders into the current Dolphin window.
  home.activation.dolphinSessionPolicy = lib.hm.dag.entryAfter [ "dolphinDefaults" ] ''
    dolphin_config=${lib.escapeShellArg "${config.xdg.configHome}/dolphinrc"}
    write_config=${lib.getExe' pkgs.kdePackages.kconfig "kwriteconfig6"}

    remember_opened_tabs=
    externally_called_folder=
    in_general=false

    if [ -r "$dolphin_config" ]; then
      while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
          '[General]')
            in_general=true
            continue
            ;;
          '['*']')
            if [ "$in_general" = true ]; then
              break
            fi
            continue
            ;;
        esac

        if [ "$in_general" = true ]; then
          case "$line" in
            RememberOpenedTabs=*)
              remember_opened_tabs="''${line#RememberOpenedTabs=}"
              ;;
            OpenExternallyCalledFolderInNewTab=*)
              externally_called_folder="''${line#OpenExternallyCalledFolderInNewTab=}"
              ;;
          esac
        fi
      done < "$dolphin_config"
    fi

    if [ "$remember_opened_tabs" != "false" ]; then
      run "$write_config" --file dolphinrc --group General --key RememberOpenedTabs false
    fi

    if [ "$externally_called_folder" != "false" ]; then
      run "$write_config" --file dolphinrc --group General --key OpenExternallyCalledFolderInNewTab false
    fi
  '';

  # Migrate only GjallarOS' previous Nextcloud icon defaults. Preserve any
  # user-selected custom icon and every unrelated Places entry.
  home.activation.dolphinNextcloudIconMigration =
    lib.mkIf nextcloudActive (
      lib.hm.dag.entryAfter [ "dolphinNextcloudPlace" ] ''
        places=${lib.escapeShellArg "${config.xdg.dataHome}/user-places.xbel"}
        nextcloud_uri="file://$HOME/Nextcloud"

        if [ -f "$places" ] &&
           ${pkgs.gnugrep}/bin/grep -Fq "href=\"$nextcloud_uri\"" "$places"
        then
          tmp="$places.gjallar-nextcloud-icon.$$"

          ${pkgs.gawk}/bin/awk \
            -v uri="$nextcloud_uri" \
            -v icon=${lib.escapeShellArg dolphinNextcloudIcons.places} '
              index($0, "<bookmark href=\"" uri "\">") {
                inside_nextcloud = 1
              }

              inside_nextcloud &&
              /<bookmark:icon name="(folder-cloud|cloudstatus)"\/>/ {
                sub(/name="(folder-cloud|cloudstatus)"/, "name=\"" icon "\"")
              }

              { print }

              inside_nextcloud && /<\/bookmark>/ {
                inside_nextcloud = 0
              }
            ' "$places" > "$tmp"

          if ${pkgs.diffutils}/bin/cmp -s "$places" "$tmp"; then
            ${pkgs.coreutils}/bin/rm -f "$tmp"
          else
            ${pkgs.coreutils}/bin/install -m 0600 "$tmp" "$places"
            ${pkgs.coreutils}/bin/rm -f "$tmp"
          fi
        fi
      ''
    );

  xdg.configFile."menus/applications.menu".source = applicationsMenu;

  # A cache built without the menu holds no apps, and KDE's own check only
  # looks at the folders it saw, so it never notices the new menu. Drop the
  # caches when the menu changes; the next KDE app builds a fresh one.
  home.activation.gjallarKdeServiceCache = lib.hm.dag.entryAfter [ "linkGeneration" ] ''
    stamp="''${XDG_STATE_HOME:-$HOME/.local/state}/gjallar/kde-applications-menu"
    if [ "$(${pkgs.coreutils}/bin/readlink "$stamp")" != ${applicationsMenu} ]; then
      run ${pkgs.coreutils}/bin/rm -f "''${XDG_CACHE_HOME:-$HOME/.cache}"/ksycoca6_*
      run ${pkgs.coreutils}/bin/mkdir -p "$(${pkgs.coreutils}/bin/dirname "$stamp")"
      run ${pkgs.coreutils}/bin/ln -sfn ${applicationsMenu} "$stamp"
    fi
  '';

  xdg.desktopEntries."org.kde.dolphin" = {
    name = "Dolphin";
    genericName = "File Manager";
    comment = "Browse files, devices, archives and network locations";
    exec = "${lib.getExe fileManager} %U";
    icon = "system-file-manager";
    terminal = false;
    categories = [
      "Qt"
      "KDE"
      "System"
      "FileManager"
    ];
    mimeType = [ "inode/directory" ];
  };

  xdg.mimeApps = {
    enable = true;
    associations.added."inode/directory" = [ "org.kde.dolphin.desktop" ];
    defaultApplications = {
      "inode/directory" = [ "org.kde.dolphin.desktop" ];
    }
    // lib.genAttrs imageTypes (_: [ "org.kde.gwenview.desktop" ])
    // lib.optionalAttrs (editorDesktop != null) (
      lib.genAttrs [
        "text/plain"
        "text/markdown"
        "application/json"
        "application/javascript"
        "application/xml"
        "application/yaml"
        "text/yaml"
      ] (_: [ editorDesktop ])
    );
  };

  programs.zsh.shellAliases.fm = "${lib.getExe fileManager} .";
}
