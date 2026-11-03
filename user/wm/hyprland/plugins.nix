{
  config,
  settings,
  lib,
  pkgs,
  ...
}:
let
  hl = import ./lua.nix { inherit lib; };
in
{
  # Not Home Manager's plugins option: with the Lua config that loads them
  # through hyprctl once the session has started. hl.plugin.load loads them
  # with the config, and permissions.nix allows exactly these.
  options.gjallar.hyprlandPlugins = lib.mkOption {
    type = lib.types.listOf lib.types.package;
    default = [ ];
    description = "Hyprland plugins loaded by the config.";
  };

  config = lib.mkMerge [
    {
      wayland.windowManager.hyprland.settings."plugin.load" = map (
        plugin: "${plugin}/lib/lib${plugin.pname}.so"
      ) config.gjallar.hyprlandPlugins;
    }

    (lib.mkIf settings.themeDetails.bordersPlusPlus {
      # Themes that ask for it get a second, wider border ring in the surface
      # colour around every window.
      gjallar.hyprlandPlugins = [ pkgs.hyprlandPlugins.borders-plus-plus ];
      # A plugin's options exist only once it has loaded, and Hyprland reads
      # the config again then; an unknown option on the first read is an error.
      wayland.windowManager.hyprland.extraConfig = ''
        if hl.get_config("plugin.borders_plus_plus.add_borders") ~= nil then
          hl.config(${
            hl.toLua {
              plugin.borders_plus_plus = {
                add_borders = 2;
                col = {
                  border_1 = hl.inline "noctaliaColors.surface";
                  border_2 = hl.inline "noctaliaColors.surface";
                };
                border_size_1 = 3;
                border_size_2 = 10;
                natural_rounding = true;
              };
            }
          })
        end
      '';
    })
  ];
}
