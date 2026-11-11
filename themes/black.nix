{ pkgs, ... }:
{
  themeName = "black-metal-immortal";
  override = null;

  btopTheme = "nord";

  opacity = 1.0;
  rounding = 6;
  shadow = false;
  bordersPlusPlus = false;
  font = "FiraCode Nerd Font"; # Selected font
  fontPkg = (pkgs.nerd-fonts.fira-code);
  fontSize = 13; # Font size

  icons = "Papirus";
  iconsPkg = pkgs.papirus-icon-theme;
}
