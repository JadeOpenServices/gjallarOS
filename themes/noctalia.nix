{ pkgs, ... }:
{
  themeName = "rose-pine";
  # Obsidian (themes/palettes/openjade.nix) in the shape the Noctalia
  # stylix-override template writes, so the first build before Noctalia
  # has run looks the same. base03/04 is the outline as Noctalia lifts it
  # to 3:1 (read from its output on e2e-fw13).
  override = {
    base00 = "0F171D";
    base01 = "16222A";
    base02 = "10191F";
    base03 = "546671";
    base04 = "546671";
    base05 = "DCE8EC";
    base06 = "DCE8EC";
    base07 = "DCE8EC";
    base08 = "E5484D";
    base09 = "85B9D8";
    base0A = "E3B464";
    base0B = "E3B464";
    base0C = "85B9D8";
    base0D = "4FA89D";
    base0E = "85B9D8";
    base0F = "E5484D";
  };

  btopTheme = null;
  shell = "noctalia";
  palette = "Obsidian";

  opacity = 0.8;
  rounding = 25;
  shadow = false;
  bordersPlusPlus = false;

  font = "FiraCode Nerd Font";
  fontPkg = pkgs.nerd-fonts.fira-code;
  uiFont = "Fira Sans";
  uiFontPkg = pkgs.fira-sans;
  fontSize = 13;

  icons = "Papirus";
  iconsPkg = pkgs.papirus-icon-theme;
}
