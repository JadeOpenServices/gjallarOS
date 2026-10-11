{
  config,
  pkgs,
  lib,
  settings,
  hyprlandShellDetails,
  ...
}:
let
  virtualKeyboard = pkgs.writeShellApplication {
    name = "gjallar-virtual-keyboard";
    runtimeInputs = [
      pkgs.procps
      pkgs.wvkbd
    ];
    text = ''
      set -euo pipefail
      # wvkbd(1): SIGUSR1 hides, SIGUSR2 shows, SIGRTMIN toggles.
      case "''${1:-toggle}" in
        show) signal=USR2 ;;
        hide) signal=USR1 ;;
        start)
          # Session start: run hidden unless something already started it.
          pgrep -x wvkbd-mobintl >/dev/null ||
            exec ${pkgs.wvkbd}/bin/wvkbd-mobintl --hidden -H 320 -L 240
          exit 0
          ;;
        *) signal=RTMIN ;;
      esac
      if pgrep -x wvkbd-mobintl >/dev/null; then
        pkill "-$signal" -x wvkbd-mobintl
      elif [ "$signal" != USR1 ]; then
        ${pkgs.wvkbd}/bin/wvkbd-mobintl -H 320 -L 240 >/dev/null 2>&1 &
        disown
      fi
    '';
  };
  # Hyprland binds a switch by its exact device name, and the names differ
  # per driver (intel-hid, intel-vbtn, thinkpad_acpi, ODDC's own switch, ...).
  # So bind every input device that reports SW_TABLET_MODE, at start, when a
  # device appears, and again after a config reload drops runtime binds.
  #
  # Hyprland reports a switch that is already on (device folded at login) once,
  # when it adds the device, before this unit can bind anything. So every name
  # also goes into a file Hyprland sources at start; from the next login on the
  # bind exists before the device does.
  tabletSwitches = "${config.xdg.stateHome}/gjallar/tablet-switches.conf";
  tabletModeBinds = pkgs.writeShellApplication {
    name = "gjallar-tablet-mode-binds";
    runtimeInputs = [
      pkgs.gnugrep
      pkgs.hyprland
      pkgs.socat
      pkgs.systemd
    ];
    text = ''
      kbd=${lib.getExe virtualKeyboard}
      known=${lib.escapeShellArg tabletSwitches}
      declare -A bound=()
      bindSwitches() {
        local caps name words
        for caps in /sys/class/input/input*/capabilities/sw; do
          [ -r "$caps" ] || continue
          read -r -a words <"$caps"
          # Lowest word last; SW_TABLET_MODE is bit 1.
          (( 16#''${words[-1]} & 2 )) || continue
          name=$(<"''${caps%/capabilities/sw}/name")
          [ -z "''${bound[$name]:-}" ] || continue
          # The name ends up inside a bind line, where a comma would start a
          # new field. USB devices pick their own names, so allow plain ones only.
          if [[ ! $name =~ ^[A-Za-z0-9\ ._()-]+$ ]]; then
            echo "skipping tablet mode switch with an unusual name: ''${name@Q}"
            continue
          fi
          bound[$name]=1
          # Hyprland already has the binds from the file since its last config load.
          if grep -qF "switch:on:$name," "$known" 2>/dev/null; then
            echo "tablet mode switch: $name (known)"
            continue
          fi
          hyprctl keyword bindl ", switch:on:$name, exec, $kbd show" >/dev/null
          hyprctl keyword bindl ", switch:off:$name, exec, $kbd hide" >/dev/null
          # By command name, not store path: the file outlives this generation.
          printf 'bindl = , switch:%s:%s, exec, gjallar-virtual-keyboard %s\n' \
            on "$name" show off "$name" hide >>"$known"
          echo "tablet mode switch: $name"
        done
      }
      bindSwitches
      sock="$XDG_RUNTIME_DIR/hypr/$HYPRLAND_INSTANCE_SIGNATURE/.socket2.sock"
      while read -r line; do
        case $line in
          configreloaded*)
            bound=()
            bindSwitches
            ;;
          UDEV*" add "*) bindSwitches ;;
        esac
      done < <(
        socat -U - "UNIX-CONNECT:$sock" &
        udevadm monitor --udev --subsystem-match=input
      )
    '';
  };
  shell = hyprlandShellDetails.binds;

  # hyprgrass binds with the text the shortcut list (Super+F1) shows for them.
  gestures = [
    {
      bind = ", edge:d:u, exec, ${lib.getExe virtualKeyboard}";
      keys = "Swipe up from the bottom edge";
      description = "Show or hide on-screen keyboard";
    }
    {
      bind = ", edge:u:d, exec, ${shell.controlCenter}";
      keys = "Swipe down from the top edge";
      description = "Open control center";
    }
    {
      bind = ", swipe:4:u, exec, ${shell.launcher}";
      keys = "Four fingers up";
      description = "Open launcher";
    }
    {
      bind = ", swipe:4:l, movetoworkspace, +1";
      keys = "Four fingers left";
      description = "Move window to next workspace";
    }
    {
      bind = ", swipe:4:r, movetoworkspace, -1";
      keys = "Four fingers right";
      description = "Move window to previous workspace";
    }
    {
      bind = ", tap:3, togglefloating, active";
      keys = "Three-finger tap";
      description = "Float or tile window";
    }
  ];
  # nixos-26.05 ships hyprgrass from 2025-10-08, which no longer compiles
  # against its Hyprland 0.55.4 (ConfigDataValues.hpp is gone). d094a3e is
  # upstream's hyprpm.toml pin for v0.55.4; drop this once nixpkgs catches up.
  hyprgrass = pkgs.hyprlandPlugins.hyprgrass.overrideAttrs {
    version = "0.8.2-unstable-2026-06-10";
    src = pkgs.fetchFromGitHub {
      owner = "horriblename";
      repo = "hyprgrass";
      rev = "d094a3e62f6ecaeb41515982d3e13edefaf8a4e7";
      hash = "sha256-tCt7FNc1RBHou/ym7B0XzoOqqNq8Df+dizEDkAgJ4U0=";
    };
    # finger-slip: keep the finger wobble tolerance when sensitivity shortens
    # swipes. border-drag: one finger dragged from a window border or gap
    # resizes at once, not only after a long press, and the long press finds
    # the border on any monitor, not just the one at (0,0).
    patches = [
      ./hyprgrass-finger-slip.patch
      ./hyprgrass-border-drag.patch
    ];
  };
in
# Touchscreen machines (the installer sets touchscreenEnable when it finds
# one): on-screen keyboard and multi-finger gestures through hyprgrass.
# Hyprland itself only knows a one-finger edge swipe on touchscreens.
# Two-finger swipes and pinches stay with the apps (scroll, zoom).
lib.mkIf (settings.touchscreenEnable or false) {
  home.packages = [
    virtualKeyboard
    pkgs.wvkbd
  ];

  gjallar.keybindHelp.gestures = map (g: { inherit (g) keys description; }) gestures ++ [
    {
      keys = "Three fingers left or right";
      description = "Switch workspace";
    }
    {
      keys = "Hold two fingers on a window and drag";
      description = "Move window";
    }
    {
      keys = "Drag a window border or gap";
      description = "Resize window";
    }
  ];

  # Detaching the keyboard or folding the screen back shows the on-screen
  # keyboard; going back to laptop mode hides it.
  systemd.user.services.gjallar-tablet-mode = {
    Unit = {
      Description = "On-screen keyboard follows tablet mode";
      After = [ "graphical-session.target" ];
      PartOf = [ "graphical-session.target" ];
    };
    Service = {
      ExecStart = lib.getExe tabletModeBinds;
      Restart = "on-failure";
      RestartSec = 5;
    };
    Install.WantedBy = [ "graphical-session.target" ];
  };

  # Hyprland reports a missing source file as a config error.
  home.activation.gjallarTabletSwitches = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    if [ ! -e ${lib.escapeShellArg tabletSwitches} ]; then
      run ${pkgs.coreutils}/bin/install -D -m 0644 /dev/null ${lib.escapeShellArg tabletSwitches}
    fi
  '';

  wayland.windowManager.hyprland = {
    plugins = [ hyprgrass ];

    settings = {
      bindd = [
        # Not plain Super+K: that already moves focus up, and Hyprland would
        # run both.
        "SUPER CTRL, K, Show or hide on-screen keyboard, exec, ${lib.getExe virtualKeyboard}"
        # KEY_KEYBOARD (374, xkb 382): the keyboard key some convertibles and
        # the ZBook Quick Keys send.
        ", code:382, Show or hide on-screen keyboard, exec, ${lib.getExe virtualKeyboard}"
      ];

      # A switch bind from the file above may have started the keyboard already.
      exec-once = [ "${lib.getExe virtualKeyboard} start" ];

      source = [ tabletSwitches ];

      plugin.touch_gestures = {
        # The default is meant for small phone screens; 4.0 is upstream's
        # advice for tablets. Swipes complete after about 40 px; the patch
        # above keeps the 100 px sideways tolerance.
        sensitivity = 4.0;
        # Three fingers left/right switch workspaces.
        workspace_swipe_fingers = 3;
        # Edge swipes are bound below, so none of them switches workspaces.
        workspace_swipe_edge = "none";
        # Wider than the 10 px default, so a finger at the bezel still hits it.
        edge_margin = 20;
        # One finger on a border or gap resizes, as resize_on_border does for
        # the mouse (upstream default, set here because the patch builds on it).
        resize_on_border_long_press = true;
      };

      hyprgrass-bind = map (g: g.bind) gestures;

      # Hold two fingers on a window and drag it.
      hyprgrass-bindm = [ ", longpress:2, movewindow" ];
    };
  };
}
