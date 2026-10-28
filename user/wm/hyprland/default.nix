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
  moniquePackage = pkgs.callPackage ../../../pkgs/monique/nix/package.nix { };

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

  # Monique writes the monitor layout as monitors.lua next to hyprland.lua,
  # which loads it last (extraConfig). A user still on the legacy
  # monitors.conf gets the layout of their active Monique profile rewritten
  # as Lua once, with Monique's own writer.
  home.activation.gjallarMoniqueDisplayState = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          config_home="''${XDG_CONFIG_HOME:-$HOME/.config}"
          monique_dir="$config_home/monique"
          settings_file="$monique_dir/settings.json"
          hypr_dir="$config_home/hypr"

          ${pkgs.coreutils}/bin/mkdir -p "$monique_dir" "$hypr_dir"

          if [ ! -f "$settings_file" ] \
            || ! ${pkgs.gnugrep}/bin/grep -Eq '"hypr_config_format"[[:space:]]*:[[:space:]]*"lua"' "$settings_file" \
            || ! ${pkgs.gnugrep}/bin/grep -Eq '"monitor_config_name"[[:space:]]*:[[:space:]]*"monitors"' "$settings_file" \
            || ${pkgs.gnugrep}/bin/grep -Eq '"config_dir"' "$settings_file"
          then
            PYTHONPATH=${moniquePackage}/${pkgs.python3.sitePackages} \
              ${pkgs.python3}/bin/python3 -I - "$settings_file" "$hypr_dir" <<'PY'
    import json
    import os
    import sys
    from pathlib import Path

    path = sys.argv[1]
    hypr_dir = Path(sys.argv[2])

    try:
        with open(path, encoding="utf-8") as handle:
            settings = json.load(handle)
    except (FileNotFoundError, json.JSONDecodeError):
        settings = {}

    if not isinstance(settings, dict):
        settings = {}

    migrating = settings.get("hypr_config_format") != "lua"
    settings["hypr_config_format"] = "lua"
    settings["monitor_config_name"] = "monitors"
    # hyprland.lua loads monitors.lua from its own directory only.
    settings.pop("config_dir", None)

    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as handle:
        json.dump(settings, handle, indent=2, sort_keys=True)
        handle.write("\n")

    os.chmod(tmp, 0o600)
    os.replace(tmp, path)

    profile_name = settings.get("active_profile")
    if migrating and isinstance(profile_name, str) and profile_name \
            and not (hypr_dir / "monitors.lua").exists():
        from monique.hypr_config import write_hyprland_configs
        from monique.profile_manager import ProfileManager

        profile = ProfileManager().load(profile_name)
        if profile is not None:
            write_hyprland_configs(profile, fmt="lua", supports_icc=True)
    PY
          fi

          ${pkgs.coreutils}/bin/rm -f \
            "$hypr_dir/monitors.conf" \
            "$hypr_dir/workspaces.conf" \
            "$hypr_dir/workspaces.lua"
  '';

  wayland.windowManager.hyprland = {
    enable = true;
    # Lua config (wiki 752 Q29): window rules can then react to a title that
    # changes after the window maps (Firefox picture-in-picture, Plane 188).
    configType = "lua";
    # Calls render in this order, so variables, plugins and permissions exist
    # before anything uses them, and binds come last.
    importantPrefixes = [
      "plugin.load"
      "permission"
      "env"
      "config"
      "curve"
      "animation"
      "monitor"
      "window_rule"
      "workspace_rule"
      "on"
      "gesture"
      "bind"
    ];
    package = pkgs.hyprland;
    # Portals come from the system module (hyprland + gtk). The HM default
    # points the portal at a per-user dir with only hyprland.portal, which
    # drops AppChooser and with it OpenURI, so xdg-open fails.
    portalPackage = null;
    systemd.enable = true;
    # Noctalia and the apps it launches run as user units. They need the
    # whole session environment, XDG_SESSION_ID for the polkit agent among it.
    systemd.variables = [ "--all" ];
    settings = {
      # Noctalia writes the theme colours as plain name=hex lines. They are
      # read as data, not run as Lua; anything else in the file is ignored and
      # a missing file keeps these defaults.
      noctaliaColors._var = lib.generators.mkLuaInline ''
        (function()
          local colors = { primary = "7aa2f7", surface = "1a1b26", secondary = "7dcfff", error = "f7768e" }
          local state = os.getenv("XDG_STATE_HOME") or ((os.getenv("HOME") or "") .. "/.local/state")
          local file = io.open(state .. "/noctalia/hyprland-colors.txt", "r")
          if file then
            for line in file:lines() do
              local name, hex = line:match("^(%a+)=(%x%x%x%x%x%x)$")
              if name and colors[name] then
                colors[name] = hex
              end
            end
            file:close()
          end
          for name, hex in pairs(colors) do
            colors[name] = "rgb(" .. hex .. ")"
          end
          return colors
        end)()'';
      config = {
        general.col = {
          active_border = lib.generators.mkLuaInline "noctaliaColors.primary";
          inactive_border = lib.generators.mkLuaInline "noctaliaColors.surface";
        };
        group.col = {
          border_active = lib.generators.mkLuaInline "noctaliaColors.secondary";
          border_inactive = lib.generators.mkLuaInline "noctaliaColors.surface";
          border_locked_active = lib.generators.mkLuaInline "noctaliaColors.error";
          border_locked_inactive = lib.generators.mkLuaInline "noctaliaColors.surface";
        };
      };
    };
    # Monique's layout overrides the default monitor rule, so it loads last.
    # A user who never saved a layout has no monitors.lua.
    extraConfig = lib.mkOrder 2000 ''
      pcall(require, "monitors")
    '';
  };

}
