# openJade desktop palettes, picked in Noctalia's settings (Appearance,
# custom palette). Source: THEMEBRAIN (Nextcloud claude/themebrain).
#
# Obsidian is the brand's dark theme as written there (section 1); tertiary
# is the brand's info blue. White, Leaf, Sakura and Ember are the dark UI
# tokens (section 0) with the family's "Jade" wordmark colour as accent;
# the Genbu Show's own versions were not at hand.
#
# Terminal colours: brand steps for the hues, white is the text 20 % toward
# the ground, bright is the colour 30 % toward the text.
# tests/nix/theme-palettes.nix holds each pair to its contrast floor.
let
  mk = p: {
    dark = {
      mPrimary = p.primary;
      mOnPrimary = p.surface;
      mSecondary = p.secondary;
      mOnSecondary = p.surface;
      mTertiary = p.tertiary;
      mOnTertiary = p.surface;
      mError = p.error;
      mOnError = p.surface;
      mSurface = p.surface;
      mOnSurface = p.onSurface;
      mSurfaceVariant = p.surfaceVariant;
      mOnSurfaceVariant = p.onSurfaceVariant;
      mOutline = p.outline;
      mShadow = p.surface;
      # One accent per screen: hover fills take the accent too.
      mHover = p.primary;
      mOnHover = p.surface;
      terminal = {
        inherit (p) normal bright;
        foreground = p.onSurface;
        background = p.surface;
        cursor = p.primary;
        cursorText = p.surface;
        selectionBg = p.primary;
        selectionFg = p.surface;
      };
    };
  };

  family =
    primary:
    mk {
      inherit primary;
      surface = "#111511";
      surfaceVariant = "#1B1F1A";
      onSurface = "#ECF0EB";
      onSurfaceVariant = "#C4CABD";
      outline = "#323731";
      secondary = "#F8B78B";
      tertiary = "#85B9D8";
      error = "#EF9497";
      normal = {
        black = "#1B1F1A";
        red = "#EF9497";
        green = "#74C08D";
        yellow = "#F8B78B";
        blue = "#85B9D8";
        magenta = "#E38FAF";
        cyan = "#4FA89D";
        white = "#C0C4BF";
      };
      bright = {
        black = "#909689";
        red = "#EEAFB0";
        green = "#98CEA9";
        yellow = "#F4C8A7";
        blue = "#A3C9DD";
        magenta = "#E5ACC1";
        cyan = "#7EBDB4";
        white = "#ECF0EB";
      };
    };
in
{
  Obsidian = mk {
    surface = "#0F171D";
    surfaceVariant = "#16222A";
    onSurface = "#DCE8EC";
    onSurfaceVariant = "#7D98A2";
    outline = "#22323C";
    primary = "#4FA89D";
    secondary = "#E3B464";
    tertiary = "#85B9D8";
    error = "#E5484D";
    normal = {
      black = "#16222A";
      red = "#E5484D";
      green = "#74C08D";
      yellow = "#E3B464";
      blue = "#85B9D8";
      magenta = "#E38FAF";
      cyan = "#4FA89D";
      white = "#B3BEC2";
    };
    bright = {
      black = "#7D98A2";
      red = "#E2787C";
      green = "#93CCA9";
      yellow = "#E0C38C";
      blue = "#9FC7DE";
      magenta = "#E0A9C1";
      cyan = "#79BBB4";
      white = "#DCE8EC";
    };
  };
  White = family "#AAB1BA";
  Leaf = family "#7BAD8E";
  Sakura = family "#EDA6C0";
  Ember = family "#F2A26F";
}
