{ pkgs, ... }:
{
  themeName = "gruvbox-material-dark-medium";
  override = null;

  btopTheme = "gruvbox_dark_v2";
  shell = "noctalia";

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
