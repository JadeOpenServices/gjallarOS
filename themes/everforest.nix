{ pkgs, ... }:
{
  themeName = "everforest-dark-hard";
  override = null;

  btopTheme = null;

  shell = "noctalia";
  opacity = 1.0;
  rounding = 6;
  shadow = false;
  bordersPlusPlus = true;
  font = "FiraCode Nerd Font"; # Selected font
  fontPkg = (pkgs.nerd-fonts.fira-code);
  fontSize = 13; # Font size

  icons = "Papirus";
  iconsPkg = pkgs.papirus-icon-theme;
}
