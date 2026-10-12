{
  config,
  pkgs,
  settings,
  inputs,
  lib,
  hyprlandShellDetails,
  ...
}:
let
  # Paste once: on a paste shortcut the clipboard is handed to a wl-copy that
  # serves exactly one read and then has nothing left to give, so it empties
  # after the app has read it, however slow the app is. A timer here lost the
  # race on slow machines (ZBook x2) and pasted nothing. Stock --paste-once
  # exits after the read, which changes the clipboard while the app is still
  # pasting; Chromium apps (Codium) then drop what they read and paste
  # nothing about half the time. The patched wl-copy stays the owner with no
  # data until the next copy replaces it. Only one type survives the
  # hand-over (text first, else the first offered), so rich formatting is
  # pasted as plain text.
  wlClipboardPasteOnce = pkgs.wl-clipboard.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./wl-copy-paste-once-keep-owner.patch ];
  });
  gjallarPasteOnce = pkgs.writeShellScript "gjallar-paste-once" ''
    set -eu
    PATH=${lib.makeBinPath [ wlClipboardPasteOnce pkgs.gnugrep pkgs.coreutils ]}

    hand_over() {
      types="$(wl-paste --list-types 2>/dev/null)" || return 0
      type="$(printf '%s\n' "$types" | grep -m1 -x 'text/plain;charset=utf-8' ||
        printf '%s\n' "$types" | grep -m1 -x 'text/plain' ||
        printf '%s\n' "$types" | head -n1)"
      [ -n "$type" ] || return 0
      wl-paste --no-newline --type "$type" |
        wl-copy --paste-once --sensitive --type "$type"
    }

    case "''${1:-}" in
      ctrl-v) keys="CTRL" key=V ;;
      ctrl-shift-v) keys="CTRL SHIFT" key=V ;;
      shift-insert) keys="SHIFT" key=Insert ;;
      *) exit 2 ;;
    esac

    hand_over
    # All three keys paste the clipboard (Ghostty is set up the same way), so
    # nothing reads the primary selection and it is dropped.
    wl-copy --primary --clear >/dev/null 2>&1 || true

    ${pkgs.hyprland}/bin/hyprctl dispatch sendshortcut "$keys,$key,activewindow" >/dev/null
  '';
  themeDetails = settings.themeDetails;
  shellDetails = hyprlandShellDetails;
  sessionStart = pkgs.writeShellScript "gjallar-hyprland-session-start" ''
    selected_wallpaper=""
    noctalia_state="''${XDG_STATE_HOME:-$HOME/.local/state}/noctalia/settings.toml"
    if [ -r "$noctalia_state" ]; then
      noctalia_wallpaper="$(${pkgs.gawk}/bin/awk '
        /^\[wallpaper\.last\]$/ { in_last = 1; next }
        /^\[/ { in_last = 0 }
        in_last && /^path[[:space:]]*=/ {
          sub(/^[^=]*=[[:space:]]*/, "")
          gsub(/^"|"$/, "")
          print
          exit
        }
      ' "$noctalia_state")"
      # The bundled wallpaper is saved by its store path, which is gone after
      # an update and garbage collection. Use the same file from the running
      # Noctalia, as pkgs/noctalia/wallpaper-store-path.patch does in the shell.
      case $noctalia_wallpaper in
        *..*) ;;
        */share/noctalia/assets/?*)
          if [ ! -e "$noctalia_wallpaper" ]; then
            noctalia_wallpaper="${config.programs.noctalia.package}/share/noctalia/assets/''${noctalia_wallpaper#*/share/noctalia/assets/}"
          fi
          ;;
      esac
      if [ -n "$noctalia_wallpaper" ] && [ -r "$noctalia_wallpaper" ]; then
        selected_wallpaper="$noctalia_wallpaper"
      fi
    fi
    # The repository ships no wallpapers: until a wallpaper is picked in
    # Noctalia the background is the theme's base colour.
    if [ -n "$selected_wallpaper" ] && [ -r "$selected_wallpaper" ]; then
      exec ${pkgs.swaybg}/bin/swaybg --image "$selected_wallpaper" --mode fill
    else
      exec ${pkgs.swaybg}/bin/swaybg --color "#${config.lib.stylix.colors.base00}"
    fi
  '';
in
{
  gjallar.keybindHelp.gestures = lib.optionals settings.touchpadWorkspaceSwipe [
    {
      keys = "Three fingers left or right on the touchpad";
      description = "Switch workspace";
    }
  ];

  home.packages = with pkgs; [
    awww
    swaybg
    wayvnc
  ];

  wayland.windowManager.hyprland.settings = {
    bindd =
      lib.optionals config.programs.noctalia.enable [
        "SUPER, C, Open control center, exec, ${lib.getExe config.programs.noctalia.package} msg status >/dev/null 2>&1 && ${lib.getExe config.programs.noctalia.package} msg panel-toggle control-center >/dev/null 2>&1 || true"
      ]
      ++ lib.optionals (settings.touchscreenEnable or false) [
        "CTRL, V, Paste, exec, ${gjallarPasteOnce} ctrl-v"
        "CTRL SHIFT, V, Paste, exec, ${gjallarPasteOnce} ctrl-shift-v"
        "SHIFT, INSERT, Paste, exec, ${gjallarPasteOnce} shift-insert"
      ];

    monitor = [
      ",preferred,auto,1"
    ];

    exec-once = lib.optionals config.programs.noctalia.enable [
      "${sessionStart}"
    ];

    general = {
      gaps_in = 8;
      gaps_out = 16;
      border_size = 2;
      allow_tearing = true;
      # Drag the gap between tiled windows (or a floating window's edge) to
      # resize it, e.g. narrow a chat column beside a stream.
      resize_on_border = true;
    };

    # A drag starts only after the mouse moved 10 px, so a plain click near a
    # border does not resize, and Super + click does not re-insert a tiled
    # window (which would reset its split). Applies to border drags too.
    binds.drag_threshold = 10;

    cursor = {
      inactive_timeout = 5;
    };

    decoration = {
      dim_special = 0.5;
      rounding = themeDetails.rounding;

      blur = {
        enabled = true;
        special = false;
        brightness = 1.0;
        contrast = 1.0;
        noise = 0.02;
        passes = 3;
        size = 10;
      };

      shadow = {
        enabled = themeDetails.shadow;
        offset = "2 2";
        range = 20;
      };
    };

    animations = {
      enabled = true;

      bezier = [
        "wind, 0.05, 0.9, 0.1, 1.05"
        "winIn, 0.1, 1.1, 0.1, 1.1"
        "winOut, 0.3, -0.3, 0, 1"
        "liner, 1, 1, 1, 1"
        "workIn, 0.72, -0.07, 0.41, 0.98"
      ];

      animation = [
        "windows, 1, 6, wind, slide"
        "windowsIn, 1, 6, winIn, slide"
        "windowsOut, 1, 5, winOut, slide"
        "windowsMove, 1, 5, wind, slide"
        "border, 1, 1, liner"
        "fade, 1, 10, default"
        "workspaces, 1, 5, wind"
        "specialWorkspace, 1, 5, workIn, slidevert"
      ];
    };

    debug = {
      disable_logs = false;
    };

    input = {
      kb_layout = settings.keyboardLayout;
      kb_variant = settings.keyboardVariant;
      kb_options = "grp:alt_shift_toggle";
      follow_mouse = true;

      touchpad = {
        natural_scroll = true;
      };

      tablet = lib.mkIf (settings.penTabletEnable or false) {
        relative_input = false;
      };
    };

    # Also listed in the shortcut list, see gjallar.keybindHelp below.
    gesture = lib.optionals settings.touchpadWorkspaceSwipe [
      "3, horizontal, workspace"
    ];

    gestures = {
      workspace_swipe_cancel_ratio = 0.15;
      workspace_swipe_forever = true;
      workspace_swipe_distance = 200;
    };

    dwindle = {
      preserve_split = true;
      force_split = 2;
      split_width_multiplier = 1.5;
    };

    misc = {
      force_default_wallpaper = -1;
      disable_hyprland_logo = true;
      disable_splash_rendering = true;
      exit_window_retains_fullscreen = true;
    };

    # Hyprland updates arrive through GjallarOS rebuilds; its release-notes
    # and donation windows only interrupt the first login after one.
    ecosystem = {
      no_update_news = true;
      no_donation_nag = true;
    };
  };
}
