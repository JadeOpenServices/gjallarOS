{
  pkgs,
  inputs,
  config,
  lib,
  osConfig ? { },
  settings,
  ...
}:
let
  themeDetails = settings.themeDetails;
  semanticTheme = import ../../../../themes/lib/semantic.nix { inherit config; };
  contrastGuard = import ../../../../themes/lib/contrast.nix { inherit pkgs; };

  # Bezel Quick Keys (ODDC's oddc-quick-keys). Only models that have them get
  # the preset indicator in the bar.
  hasQuickKeys =
    lib.attrByPath [ "oddc" "resolved" "hardware" "input" "quickKeys" ] { } osConfig ? primary;
  # On-screen keyboard button, same switch as user/wm/hyprland/touch.nix.
  hasTouchKeyboard = settings.touchscreenEnable or false;
  # The shield until disk protection is set up (system/security/disk-unlock.nix).
  hasEncryptedDisk = lib.attrByPath [ "boot" "initrd" "luks" "devices" ] { } osConfig != { };
  # The notice that stays while developer mode is on (ROADMAP section 14).
  # Turning it on or off rebuilds, so the build knows.
  hasDeveloperMode = settings.developerMode or false;
  # The shield while the VPN blocks or pauses this network (apps/tailscale).
  hasVPN = config.gjallar.apps.tailscale.enable or false;
  gjallarctlPackage = pkgs.callPackage ../../../../pkgs/gjallarctl { };

  palette = {
    dark = {
      mPrimary = "#${semanticTheme.fallback.primary}";
      mOnPrimary = "#${semanticTheme.fallback.onPrimary}";
      mSecondary = "#${semanticTheme.fallback.secondary}";
      mOnSecondary = "#${semanticTheme.fallback.onSecondary}";
      mTertiary = "#${semanticTheme.fallback.tertiary}";
      mOnTertiary = "#${semanticTheme.fallback.onTertiary}";
      mError = "#${semanticTheme.fallback.error}";
      mOnError = "#${semanticTheme.fallback.onError}";
      mSurface = "#${semanticTheme.fallback.surface}";
      mOnSurface = "#${semanticTheme.fallback.onSurface}";
      mSurfaceVariant = "#${semanticTheme.fallback.surfaceVariant}";
      mOnSurfaceVariant = "#${semanticTheme.fallback.onSurfaceVariant}";
      mOutline = "#${semanticTheme.fallback.outline}";
      mShadow = "#${semanticTheme.fallback.shadow}";
      mHover = "#${semanticTheme.fallback.hover}";
      mOnHover = "#${semanticTheme.fallback.onHover}";

      terminal = {
        background = "#${semanticTheme.fallback.terminal.background}";
        foreground = "#${semanticTheme.fallback.terminal.foreground}";

        cursor = "#${semanticTheme.fallback.terminal.cursor}";
        cursorText = "#${semanticTheme.fallback.terminal.cursorText}";

        selectionBg = "#${semanticTheme.fallback.selection}";
        selectionFg = "#${semanticTheme.fallback.onSelection}";

        normal = {
          black = "#${semanticTheme.fallback.terminal.normal.black}";
          red = "#${semanticTheme.fallback.terminal.normal.red}";
          green = "#${semanticTheme.fallback.terminal.normal.green}";
          yellow = "#${semanticTheme.fallback.terminal.normal.yellow}";
          blue = "#${semanticTheme.fallback.terminal.normal.blue}";
          magenta = "#${semanticTheme.fallback.terminal.normal.magenta}";
          cyan = "#${semanticTheme.fallback.terminal.normal.cyan}";
          white = "#${semanticTheme.fallback.terminal.normal.white}";
        };

        bright = {
          black = "#${semanticTheme.fallback.terminal.bright.black}";
          red = "#${semanticTheme.fallback.terminal.bright.red}";
          green = "#${semanticTheme.fallback.terminal.bright.green}";
          yellow = "#${semanticTheme.fallback.terminal.bright.yellow}";
          blue = "#${semanticTheme.fallback.terminal.bright.blue}";
          magenta = "#${semanticTheme.fallback.terminal.bright.magenta}";
          cyan = "#${semanticTheme.fallback.terminal.bright.cyan}";
          white = "#${semanticTheme.fallback.terminal.bright.white}";
        };
      };
    };
  };
in
{
  imports = [
    ./gtk.nix
    ./qt.nix
  ];

  programs.noctalia = {
    enable = true;
    # A user unit, so home-manager restarts the shell when a rebuild changes
    # it. Started from the Hyprland session it kept running the old build
    # until the next login.
    systemd.enable = true;
    package = pkgs.callPackage ../../../../pkgs/noctalia {
      noctalia = inputs.noctalia.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };

    settings =
      let
        base = import ./settings.nix;
      in
      lib.recursiveUpdate base {

        theme = {
          mode = "dark";
          source = "builtin";
          builtin = "Noctalia";
          templates.user.hyprland = {
            input_path = "$XDG_CONFIG_HOME/noctalia/templates/hyprland-colors.txt";
            output_path = "$XDG_STATE_HOME/noctalia/hyprland-colors.txt";
            post_hook = "${pkgs.hyprland}/bin/hyprctl reload; if ${lib.getExe config.programs.noctalia.package} msg status >/dev/null 2>&1; then ${lib.getExe config.programs.noctalia.package} msg greeter-sync >/dev/null 2>&1 || true; fi";
          };
          templates.user.ghostty = {
            input_path = "$XDG_CONFIG_HOME/noctalia/templates/ghostty.conf";
            output_path = "$XDG_CONFIG_HOME/ghostty/themes/noctalia";
            pre_hook = contrastGuard.preHook;
            post_hook = "${pkgs.systemd}/bin/systemctl reload --user app-com.mitchellh.ghostty.service >/dev/null 2>&1 || true";
          };
          templates.user.gjallar_plymouth = {
            input_path = "$XDG_CONFIG_HOME/noctalia/templates/stylix-override.json";
            output_path = "$XDG_STATE_HOME/noctalia/stylix-override.json";
          };
        };
        accessibility = {
          ui_scale = 1.4;
        };
        # battery-guard (system/hardware/battery.nix, same ODDC switch) shows the
        # low and critical battery dialogs; a second toast for them is clutter.
        battery.notify_system_battery =
          !(lib.attrByPath [ "oddc" "resolved" "class" "capabilities" "battery" ] false osConfig);
        plugins.enabled =
          lib.optional hasQuickKeys "gjallar/quick-keys"
          ++ lib.optional hasTouchKeyboard "gjallar/osk"
          ++ lib.optional hasEncryptedDisk "gjallar/disk-protection"
          ++ lib.optional hasDeveloperMode "gjallar/developer-mode"
          ++ lib.optional hasVPN "gjallar/vpn"
          ++ [ "gjallar/time" ];
        shell = {
          clipboard_enabled = false;
          corner_radius_scale = 2.0;
          font_family = themeDetails.font;
          polkit_agent = true;
          # No "Welcome to Noctalia" wizard: its choices are saved as overrides
          # that beat the theme above and the colors it feeds to Hyprland,
          # Ghostty and Plymouth. Theme and wallpaper can still be changed in
          # Noctalia's settings.
          setup_wizard_enabled = false;
          telemetry_enabled = false;
          # Apps get their own unit, so a shell restart after a rebuild does
          # not take them down with it.
          launch_apps_as_systemd_services = true;
          greeter_sync = {
            auto_sync = true;
            # gjallar-greeter-sync-privilege: system/wm/common/wayland.nix
            privilege_command = "/run/current-system/sw/bin/gjallar-greeter-sync-privilege";
          };
          screenshot = {
            directory = "${config.home.homeDirectory}/Pictures/Screenshots";
          };
          panel = {
            clipboard_placement = "attached";
            open_near_click_clipboard = true;
            open_near_click_control_center = true;
          };
        };
        widget = {
          launcher = {
            glyph = "skull";
            scale = 1.25;
          };
          keyboard_layout = {
            scale = 0.98;
            show_glyph = false;
          };
          volume = {
            scale = 0.98;
            show_label = false;
          };
          spacer_default = {
            type = "spacer";
            length = 15;
          };
          audio_visualizer = {
            width = 120;
          };
          tray = {
            drawer = false;
            menu_on_left_click = [ "nextcloud" ];
            trailing_separator = true;
          };
          cpu_usage = {
            type = "sysmon";
            stat = "cpu_usage";
            scale = 0.98;
          };

          cpu_temp = {
            type = "sysmon";
            stat = "cpu_temp";
            scale = 0.98;
          };

          # System RAM. gpu_vram showed the APU's fixed 2 GiB carveout,
          # which sits near 88% and looked like a stuck RAM reading.
          ram = {
            type = "sysmon";
            stat = "ram_pct";
            scale = 0.98;
          };

          power_profile = {
            type = "power_profile";
            scale = 1.0;

            actions = {
              left = "power-cycle";

              right = "panel-toggle control-center power";
            };

          };

          battery = {
            type = "battery";
            display_mode = "glyph";
            show_label = true;
            label_content = "percent";
            scale = 0.98;
          };

          divider_battery = {
            type = "text";
            text = "│";
            scale = 0.72;
            font_scale = 0.9;
            interactive = false;
          };

          divider_apps = {
            type = "text";
            text = "│";
            scale = 0.72;
            font_scale = 0.9;
            interactive = false;
          };

          divider_media = {
            type = "text";
            text = "│";
            scale = 0.72;
            font_scale = 0.9;
            interactive = false;
          };

          # Dots for the other used workspaces; the focused one grows into a
          # pill with the active app's icon, so no separate window widget.
          workspaces = {
            style = "focus_hint";
            show_labels = false;
            hide_when_empty = true;
            scale = 0.98;
          };

          weather = {
            type = "weather";
            scale = 0.98;
          };

          media = {
            hide_when_no_media = true;
            type = "media";

            art_size = 16;

            scale = 0.98;
          };

          session = {
            type = "session";
            scale = 0.98;

            actions = {
              left = "panel-toggle session";
            };
          };

          divider_center_left = {
            type = "text";
            text = "│";
            scale = 0.72;
            font_scale = 0.9;
            interactive = false;
          };

          time_status = {
            type = "gjallar/time:status";
            scale = 0.98;
          };

          center_clock = {
            type = "clock";
            format = "{:%a %d %b  %H:%M}";
            scale = 0.97;
            font_scale = 1.0;
            anchor = true;
          };

        }
        # A plugin's widgets exist only while the plugin is enabled; Noctalia
        # warns about a widget whose type it does not know.
        // lib.optionalAttrs hasEncryptedDisk {
          disk_protection = {
            type = "gjallar/disk-protection:status";
            scale = 0.98;
          };
        }
        // lib.optionalAttrs hasVPN {
          vpn = {
            type = "gjallar/vpn:status";
            scale = 0.98;
          };
        }
        // lib.optionalAttrs hasDeveloperMode {
          developer_mode = {
            type = "gjallar/developer-mode:status";
            scale = 0.98;
          };
        }
        // lib.optionalAttrs hasQuickKeys {
          quick_keys = {
            type = "gjallar/quick-keys:preset";
            scale = 0.98;
          };
        }
        // lib.optionalAttrs hasTouchKeyboard {
          touch_keyboard = {
            type = "gjallar/osk:button";
            scale = 0.98;
          };
        };

        location = {
          auto_locate = false;
          address = "${settings.weatherCity}, ${settings.weatherCountry}";
        };
        osd = {
          orientation = "horizontal";
          position = "top_center";
          kinds = {
            keyboard_layout = false;
          };
        };
        weather.enabled = true;

        system.monitor = {
          enabled = true;

          cpu_poll_seconds = 2.0;

          gpu_poll_seconds = 5.0;

          memory_poll_seconds = 2.0;
          network_poll_seconds = 3.0;
          disk_poll_seconds = 10.0;
        };

        bar.default = {
          capsule_thickness = 0.78;
          font_scale = 1.0;
          widget_spacing = 5;
          scale = 1.06;
          thickness = 40;
          margin_edge = 0;
          margin_ends = 20;
          margin_opposite_edge = 0;

          padding = 14;

          radius = 12;
          radius_top_left = 20;
          radius_top_right = 20;

          radius_bottom_left = 12;
          radius_bottom_right = 12;
          concave_edge_corners = true;
          background_opacity = 0.94;
          shadow = false;

          # System status on the left, workspaces and time in the middle,
          # app icons, controls and battery on the right.
          start = [
            "cpu_usage"
            "cpu_temp"
            "ram"
          ];
          center = [
            "workspaces"
            "divider_center_left"
            "center_clock"
          ];
          end = [
            "tray"
            "network"
          ]
          ++ lib.optional hasDeveloperMode "developer_mode"
          ++ lib.optional hasEncryptedDisk "disk_protection"
          ++ lib.optional hasVPN "vpn"
          ++ [ "time_status" ]
          ++ lib.optional hasQuickKeys "quick_keys"
          ++ lib.optional hasTouchKeyboard "touch_keyboard"
          ++ [
            "keyboard_layout"
            "notifications"
            "clipboard"
            "volume"
            "divider_battery"
            "power_profile"
            "battery"
            "divider_apps"
            "weather"
            "divider_media"
            "media"
            "session"
          ];
        };
        wallpaper = {
          directory = "${config.home.homeDirectory}/Pictures";
          automation.recursive = true;
          per_monitor_directories = false;
          fill_mode = "crop";
          transition_on_startup = false;
        };
      };
  };

  xdg.configFile."noctalia/palettes/stylix.json".text = builtins.toJSON palette;

  # Noctalia picks up plugins in its local source dir; enabled above.
  xdg.dataFile."noctalia/plugins/gjallar-quick-keys" = lib.mkIf hasQuickKeys {
    source = ./plugins/quick-keys;
  };
  xdg.dataFile."noctalia/plugins/gjallar-osk" = lib.mkIf hasTouchKeyboard {
    source = ./plugins/osk;
  };
  xdg.dataFile."noctalia/plugins/gjallar-disk-protection" = lib.mkIf hasEncryptedDisk {
    source = ./plugins/disk-protection;
  };
  xdg.dataFile."noctalia/plugins/gjallar-developer-mode" = lib.mkIf hasDeveloperMode {
    source = ./plugins/developer-mode;
  };
  xdg.dataFile."noctalia/plugins/gjallar-vpn" = lib.mkIf hasVPN {
    source = ./plugins/vpn;
  };
  xdg.dataFile."noctalia/plugins/gjallar-time".source = ./plugins/time;
  # Noctalia loads plugin scripts once at start; a changed plugin restarts
  # the bar like a changed config.toml does.
  systemd.user.services.noctalia.Unit.X-Restart-Triggers =
    lib.optional hasQuickKeys "${./plugins/quick-keys}"
    ++ lib.optional hasTouchKeyboard "${./plugins/osk}"
    ++ lib.optional hasEncryptedDisk "${./plugins/disk-protection}"
    ++ lib.optional hasDeveloperMode "${./plugins/developer-mode}"
    ++ lib.optional hasVPN "${./plugins/vpn}"
    ++ [ "${./plugins/time}" ];

  # The developer-mode notice's click: the status in a terminal, readable
  # until Enter.
  systemd.user.services.developer-mode-status = lib.mkIf hasDeveloperMode {
    Unit.Description = "Developer mode status";
    Service = {
      Type = "exec";
      ExecStart = lib.escapeShellArgs [
        "${pkgs.ghostty}/bin/ghostty"
        "--title=GjallarOS developer mode"
        "-e"
        (pkgs.writeShellScript "developer-mode-status" ''
          ${gjallarctlPackage}/bin/gjallarctl system status
          printf '\nPress Enter to close. '
          read -r _
        '')
      ];
    };
  };

  # The clock in the bar and the card after 24 hours without NTS
  # (ROADMAP section 18). Reads only what every user may read.
  systemd.user.services.gjallar-time-watch = {
    Unit = {
      Description = "Watch whether NTS confirms the time";
      After = [ "hyprland-session.target" ];
      PartOf = [ "hyprland-session.target" ];
    };
    Service = {
      Type = "exec";
      ExecStart = "${gjallarctlPackage}/bin/gjallarctl system time-watch";
      Environment = [ "PATH=${lib.makeBinPath [ pkgs.chrony ]}:/run/current-system/sw/bin" ];
      Restart = "on-failure";
    };
    Install.WantedBy = [ "hyprland-session.target" ];
  };

  # The clock's click and the card's Details.
  systemd.user.services.gjallar-time-details = {
    Unit.Description = "Time details";
    Service = {
      Type = "exec";
      ExecStart = lib.escapeShellArgs [
        "${pkgs.ghostty}/bin/ghostty"
        "--title=GjallarOS time"
        "--window-width=84"
        "--window-height=12"
        "-e"
        (pkgs.writeShellScript "gjallar-time-details" ''
          PATH=${lib.makeBinPath [ pkgs.chrony ]}:/run/current-system/sw/bin
          ${gjallarctlPackage}/bin/gjallarctl system time
          printf '\nPress Enter to close. '
          read -r _
        '')
      ];
    };
  };

  # Plain data: hyprland.lua reads these lines and never runs the file.
  xdg.configFile."noctalia/templates/hyprland-colors.txt".text = ''
    primary={{colors.primary.default.hex_stripped}}
    surface={{colors.surface.default.hex_stripped}}
    secondary={{colors.secondary.default.hex_stripped}}
    error={{colors.error.default.hex_stripped}}
  '';

  xdg.configFile."noctalia/templates/ghostty.conf".text = ''
    background = {{colors.surface.default.hex}}
    foreground = {{colors.on_surface.default.hex}}
    cursor-color = {{colors.primary.default.hex}}
    cursor-text = {{colors.on_primary.default.hex}}
    selection-background = {{colors.primary.default.hex}}
    selection-foreground = {{colors.on_primary.default.hex}}
    palette = 0={{colors.surface.default.hex}}
    palette = 1={{colors.error.default.hex}}
    palette = 2={{colors.secondary.default.hex}}
    palette = 3={{colors.tertiary.default.hex}}
    palette = 4={{colors.primary.default.hex}}
    palette = 5={{colors.tertiary.default.hex}}
    palette = 6={{colors.secondary.default.hex}}
    palette = 7={{colors.on_surface.default.hex}}
    palette = 8={{colors.outline.default.hex}}
    palette = 9={{colors.error.default.hex}}
    palette = 10={{colors.secondary.default.hex}}
    palette = 11={{colors.tertiary.default.hex}}
    palette = 12={{colors.primary.default.hex}}
    palette = 13={{colors.tertiary.default.hex}}
    palette = 14={{colors.secondary.default.hex}}
    palette = 15={{colors.on_surface.default.hex}}
  '';

  xdg.configFile."noctalia/templates/stylix-override.json".text = ''
    {
      "base00": "{{colors.surface.default.hex_stripped}}",
      "base01": "{{colors.surface_variant.default.hex_stripped}}",
      "base02": "{{colors.surface_container_lowest.default.hex_stripped}}",
      "base03": "{{colors.outline.default.hex_stripped}}",
      "base04": "{{colors.outline.default.hex_stripped}}",
      "base05": "{{colors.on_surface.default.hex_stripped}}",
      "base06": "{{colors.on_surface.default.hex_stripped}}",
      "base07": "{{colors.on_surface.default.hex_stripped}}",
      "base08": "{{colors.error.default.hex_stripped}}",
      "base09": "{{colors.tertiary.default.hex_stripped}}",
      "base0A": "{{colors.secondary.default.hex_stripped}}",
      "base0B": "{{colors.secondary.default.hex_stripped}}",
      "base0C": "{{colors.tertiary.default.hex_stripped}}",
      "base0D": "{{colors.primary.default.hex_stripped}}",
      "base0E": "{{colors.tertiary.default.hex_stripped}}",
      "base0F": "{{colors.error.default.hex_stripped}}"
    }
  '';

  systemd.user.services.noctalia.Service.ExecStartPre = [
    # A shell started by the session before this unit existed holds the
    # single-instance lock, so the unit would fail until the next login.
    # Take its environment (XDG_SESSION_ID for logind and polkit; that
    # session did not import it) and stop it; the unit takes over.
    "${pkgs.writeShellScript "noctalia-take-over" ''
      lock="''${XDG_RUNTIME_DIR:-/tmp}/noctalia-''${WAYLAND_DISPLAY:-wayland-0}.lock"
      [ -e "$lock" ] || exit 0
      ${pkgs.util-linux}/bin/flock -n "$lock" true && exit 0
      for pid in $(${pkgs.psmisc}/bin/fuser "$lock" 2>/dev/null); do
        ${pkgs.gnugrep}/bin/grep -zE '^[A-Za-z_][A-Za-z0-9_]*=' "/proc/$pid/environ" \
          | ${pkgs.gnugrep}/bin/grep -zvE '^(_|SHLVL|PWD|OLDPWD)=' \
          | ${pkgs.findutils}/bin/xargs -0 -r ${pkgs.systemd}/bin/systemctl --user set-environment || true
        kill -TERM "$pid" 2>/dev/null || true
      done
      ${pkgs.util-linux}/bin/flock -w 15 "$lock" true || true
    ''}"
    # Weather and plugins fetch at start; give the network a moment first.
    "-${pkgs.networkmanager}/bin/nm-online -q -t 10"
  ];
}
