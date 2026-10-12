{
  inputs,
  config,
  pkgs,
  lib,
  options,
  settings,
  ...
}:
let
  # Rotate only when ODDC proves an orientation sensor. iio-sensor-proxy alone
  # says nothing: ODDC may run it for a light sensor. Older ODDC pins have no
  # such option; there the installer's flag still decides.
  orientationSensor =
    if lib.hasAttrByPath [ "oddc" "sensors" "orientation" "present" ] options then
      config.oddc.sensors.orientation.present
    else
      settings.orientationSensorEnable or false;
  # ODDC's own tablet mode switch (ODDC 35.3). Pins without the
  # oddc.tabletMode option still look for it; a model ODDC says has none
  # rotates freely, and one it says has one treats a missing switch as laptop
  # mode.
  tabletSwitchName = "ODDC Tablet Mode Switch";
  tabletModeKnown = lib.hasAttrByPath [ "oddc" "tabletMode" "present" ] options;
  tabletSwitchArgs =
    lib.optionals (!tabletModeKnown || config.oddc.tabletMode.present) [
      "--tablet-switch"
      tabletSwitchName
    ]
    ++ lib.optional (tabletModeKnown && config.oddc.tabletMode.present) "--require-tablet-switch";
  # The rotation service reads the switch as the logged-in user. uaccess must
  # be tagged before 73-seat-late.rules applies it, so this cannot go into
  # extraRules (99-local). systemd's own 70-uaccess.rules does the same for
  # joysticks. ODDC creates the switch through uinput, so only a virtual
  # device qualifies; a USB device picks its own name.
  tabletSwitchAccess = pkgs.writeTextFile {
    name = "gjallar-tablet-switch-udev-rules";
    destination = "/lib/udev/rules.d/70-gjallar-tablet-switch.rules";
    text = ''
      SUBSYSTEM=="input", KERNEL=="event*", DEVPATH=="/devices/virtual/input/*", ENV{ID_INPUT_SWITCH}=="1", ENV{ID_INPUT_KEY}!="1", ATTRS{name}=="${tabletSwitchName}", TAG+="uaccess"
    '';
  };
  gjallarctl = pkgs.callPackage ../../../pkgs/gjallarctl { };
  moniquePackage = pkgs.callPackage ../../../pkgs/monique/nix/package.nix { };
  hyprlandSession = pkgs.writeShellScriptBin "gjallar-hyprland-session" ''
        state_dir="''${XDG_STATE_HOME:-$HOME/.local/state}/hyprland"
        noctalia_state_dir="''${XDG_STATE_HOME:-$HOME/.local/state}/noctalia"
        hypr_config_dir="''${XDG_CONFIG_HOME:-$HOME/.config}/hypr"
        mkdir -p "$state_dir"
        mkdir -p "$noctalia_state_dir" "$hypr_config_dir"

        if [ ! -e "$noctalia_state_dir/hyprland-colors.conf" ]; then
          cat >"$noctalia_state_dir/hyprland-colors.conf" <<'EOF'
    $primary = rgb(7aa2f7)
    $surface = rgb(1a1b26)
    $secondary = rgb(7dcfff)
    $error = rgb(f7768e)

    general {
      col.active_border = $primary
      col.inactive_border = $surface
    }

    group {
      col.border_active = $secondary
      col.border_inactive = $surface
      col.border_locked_active = $error
      col.border_locked_inactive = $surface
    }
    EOF
        fi
        for display_config in monitors.conf workspaces.conf; do
          [ -e "$hypr_config_dir/$display_config" ] || : >"$hypr_config_dir/$display_config"
        done

        export HYPRLAND_CONFIG="$hypr_config_dir/hyprland.conf"
        exec ${pkgs.hyprland}/bin/start-hyprland >>"$state_dir/startup.log" 2>&1
  '';
  hyprlandSessionEntry =
    pkgs.runCommand "gjallar-hyprland-session-entry"
      {
        passthru.providedSessions = [ "gjallar-hyprland" ];
      }
      ''
        install -Dm444 ${pkgs.writeText "gjallar-hyprland.desktop" ''
          [Desktop Entry]
          Name=GjallarOS Hyprland
          Comment=Hyprland session with quiet startup logging
          Exec=${hyprlandSession}/bin/gjallar-hyprland-session
          Type=Application
          DesktopNames=Hyprland
        ''} "$out/share/wayland-sessions/gjallar-hyprland.desktop"
      '';
in
{
  environment.systemPackages = [ hyprlandSession ];

  services.displayManager.sessionPackages = [ hyprlandSessionEntry ];

  systemd.user.services.display-input-rotation = lib.mkIf orientationSensor {
    description = "Automatic display and input rotation";

    wantedBy = [ "graphical-session.target" ];
    after = [ "graphical-session.target" ];
    # hyprland-rotate runs monitor-sensor and hyprctl.
    path = [
      pkgs.iio-sensor-proxy
      config.programs.hyprland.package
    ];

    serviceConfig = {
      ExecStart = lib.escapeShellArgs (
        [
          "${gjallarctl}/bin/gjallarctl"
          "hyprland-rotate"
        ]
        ++ tabletSwitchArgs
      );
      Restart = "on-failure";
      RestartSec = 1;
    };
  };

  services.udev.packages = lib.mkIf (orientationSensor && tabletSwitchArgs != [ ]) [
    tabletSwitchAccess
  ];

  imports = [
    ../common/wayland.nix
  ];

  programs = {
    monique = {
      enable = true;
      package = moniquePackage;
      enablePolkit = false;
    };

    hyprland = {
      enable = true;
      xwayland.enable = true;
      package = pkgs.hyprland;
      portalPackage = pkgs.xdg-desktop-portal-hyprland;
    };
  };

  # programs.hyprland already adds the hyprland and gtk portals. Listing gtk here
  # again put its D-Bus services on the session bus twice, and dbus-broker
  # logged "Ignoring duplicate name" for each of them on every reload.
  xdg.portal = {
    enable = true;
    xdgOpenUsePortal = true;
    config.hyprland = {
      default = [
        "hyprland"
        "gtk"
      ];
      "org.freedesktop.impl.portal.AppChooser" = [ "gtk" ];
      "org.freedesktop.impl.portal.FileChooser" = [ "gtk" ];
    };
  };
}
