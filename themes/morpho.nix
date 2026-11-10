{ pkgs, ... }:
{
  themeName = "rose-pine";
  override = {
    base00 = "05000f";
  };

  btopTheme = "gruvbox_dark_v2";

  opacity = 1.0;
  rounding = 6;
  shadow = false;
  bordersPlusPlus = false;

  font = "IosevkaTerm NF"; # Selected font
  fontPkg = (pkgs.nerd-fonts.iosevka-term);
  fontSize = 16; # Font size

  icons = "Papirus";
  iconsPkg = pkgs.papirus-icon-theme;
}
