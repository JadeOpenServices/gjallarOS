{
  inputs,
  config,
  lib,
  settings,
  pkgs,
  ...
}:
let
  shell = settings.themeDetails.shell or "noctalia";
  sessionEnvironment = lib.concatStringsSep " && " [
    "${pkgs.coreutils}/bin/env -u HL_INITIAL_WORKSPACE_TOKEN ${pkgs.dbus}/bin/dbus-update-activation-environment --systemd --all"
    "${pkgs.systemd}/bin/systemctl --user stop hyprland-session.target"
    "${pkgs.systemd}/bin/systemctl --user start hyprland-session.target"
  ];

in
{
  _module.args.hyprlandShellDetails = import (../. + "/shells/${shell}/details.nix") {
    inherit
      pkgs
      inputs
      lib
      config
      ;
  };
  imports = [
    ./env.nix
    ./binds.nix
    ./rules.nix
    ./plugins.nix
    ./touch.nix
    ./hyprlock.nix
    ./permissions.nix
    ../shells/${shell}
    ./settings.nix
  ];

  home.packages = with pkgs; [
    hyprcursor
  ];

  home.activation.gjallarMoniqueDisplayState = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          config_home="''${XDG_CONFIG_HOME:-$HOME/.config}"
          monique_dir="$config_home/monique"
          settings_file="$monique_dir/settings.json"
          hypr_dir="$config_home/hypr"

          ${pkgs.coreutils}/bin/mkdir -p "$monique_dir" "$hypr_dir"

          if [ ! -f "$settings_file" ] \
            || ! ${pkgs.gnugrep}/bin/grep -Eq '"hypr_config_format"[[:space:]]*:[[:space:]]*"legacy"' "$settings_file" \
            || ! ${pkgs.gnugrep}/bin/grep -Eq '"monitor_config_name"[[:space:]]*:[[:space:]]*"monitors"' "$settings_file"
          then
            ${pkgs.python3}/bin/python3 - "$settings_file" <<'PY'
    import json
    import os
    import sys

    path = sys.argv[1]

    try:
        with open(path, encoding="utf-8") as handle:
            settings = json.load(handle)
    except (FileNotFoundError, json.JSONDecodeError):
        settings = {}

    if not isinstance(settings, dict):
        settings = {}

    settings["hypr_config_format"] = "legacy"
    settings["monitor_config_name"] = "monitors"

    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as handle:
        json.dump(settings, handle, indent=2, sort_keys=True)
        handle.write("\n")

    os.chmod(tmp, 0o600)
    os.replace(tmp, path)
    PY
          fi

          ${pkgs.coreutils}/bin/rm -f \
            "$hypr_dir/monitors.lua" \
            "$hypr_dir/workspaces.conf" \
            "$hypr_dir/workspaces.lua"

          if [ ! -e "$hypr_dir/monitors.conf" ]; then
            : > "$hypr_dir/monitors.conf"
          fi
  '';

  wayland.windowManager.hyprland = {
    enable = true;
    configType = "hyprlang";
    package = pkgs.hyprland;
    # Portals come from the system module (hyprland + gtk). The HM default
    # points the portal at a per-user dir with only hyprland.portal, which
    # drops AppChooser and with it OpenURI, so xdg-open fails.
    portalPackage = null;
    # The session start below replaces Home Manager's systemd integration.
    systemd.enable = false;
    # Noctalia and the apps it launches run as user units. They need the
    # whole session environment, XDG_SESSION_ID for the polkit agent among
    # it, so everything is handed to systemd and D-Bus but one variable.
    # Hyprland gives every command it starts its own HL_INITIAL_WORKSPACE_TOKEN,
    # which places that command's first window on the workspace it was
    # started from. Exported, this command's token (workspace 1 at login)
    # would reach every user service, and the first window any of them
    # opens (USB review, setup windows) would land on workspace 1 instead
    # of the one in use (Plane 279). Home Manager's own start command has
    # no way to leave a variable out, so this replaces it.
    settings.exec-once = lib.mkBefore [ sessionEnvironment ];
    extraConfig = ''
      source = ~/.local/state/noctalia/hyprland-colors.conf
      source = ~/.config/hypr/monitors.conf
    '';
  };

  # Home Manager's hyprland-session target, which the start command above
  # starts and the session's user units hang off.
  systemd.user.targets.hyprland-session.Unit = {
    Description = "Hyprland compositor session";
    Documentation = [ "man:systemd.special(7)" ];
    BindsTo = [ "graphical-session.target" ];
    Wants = [ "graphical-session-pre.target" ];
    After = [ "graphical-session-pre.target" ];
  };
}
