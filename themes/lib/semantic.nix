{ config }:
let
  colors = config.lib.stylix.colors;

  tokens = {
    surface = "surface";
    onSurface = "on_surface";

    surfaceVariant = "surface_variant";
    onSurfaceVariant = "on_surface_variant";

    surfaceContainerLowest = "surface_container_lowest";
    surfaceContainerLow = "surface_container_low";
    surfaceContainer = "surface_container";
    surfaceContainerHigh = "surface_container_high";
    surfaceContainerHighest = "surface_container_highest";

    primary = "primary";
    onPrimary = "on_primary";

    secondary = "secondary";
    onSecondary = "on_secondary";

    tertiary = "tertiary";
    onTertiary = "on_tertiary";

    error = "error";
    onError = "on_error";

    outline = "outline";
    outlineVariant = "outline_variant";

    hover = "surface_container_high";
    onHover = "on_surface";

    selection = "primary";
    onSelection = "on_primary";
  };

  fallback = {
    surface = colors.base00;
    onSurface = colors.base05;

    surfaceVariant = colors.base01;
    onSurfaceVariant = colors.base05;

    surfaceContainerLowest = colors.base00;
    surfaceContainerLow = colors.base01;
    surfaceContainer = colors.base01;
    surfaceContainerHigh = colors.base02;
    surfaceContainerHighest = colors.base02;

    primary = colors.base0D;
    onPrimary = colors.base00;

    secondary = colors.base0A;
    onSecondary = colors.base00;

    tertiary = colors.base0C;
    onTertiary = colors.base00;

    error = colors.base08;
    onError = colors.base00;

    outline = colors.base04;
    outlineVariant = colors.base03;

    shadow = colors.base00;

    hover = colors.base02;
    onHover = colors.base05;

    selection = colors.base0D;
    onSelection = colors.base00;

    terminal = {
      background = colors.base00;
      foreground = colors.base05;
      # Terminal cursor is an interactive accent pair.
      cursor = colors.base0D;
      cursorText = colors.base00;

      normal = {
        black = colors.base00;
        red = colors.base08;
        green = colors.base0B;
        yellow = colors.base0A;
        blue = colors.base0D;
        magenta = colors.base0E;
        cyan = colors.base0C;
        white = colors.base05;
      };

      bright = {
        black = colors.base03;
        red = colors.base08;
        green = colors.base0B;
        yellow = colors.base0A;
        blue = colors.base0D;
        magenta = colors.base0E;
        cyan = colors.base0C;
        white = colors.base07;
      };

      # Base16 colors outside the conventional ANSI 16-color mapping that
      # terminal UIs may still use for secondary accents.
      extended = {
        light = colors.base06;
        orange = colors.base09;
      };
    };
  };

  # Foreground/background pairs are owned centrally. Consumers should use
  # these relationships instead of independently choosing text colors.
  pairs = {
    surface = {
      background = tokens.surface;
      foreground = tokens.onSurface;
    };

    surfaceVariant = {
      background = tokens.surfaceVariant;
      foreground = tokens.onSurfaceVariant;
    };

    primary = {
      background = tokens.primary;
      foreground = tokens.onPrimary;
    };

    secondary = {
      background = tokens.secondary;
      foreground = tokens.onSecondary;
    };

    tertiary = {
      background = tokens.tertiary;
      foreground = tokens.onTertiary;
    };

    error = {
      background = tokens.error;
      foreground = tokens.onError;
    };

    hover = {
      background = tokens.hover;
      foreground = tokens.onHover;
    };

    selection = {
      background = tokens.selection;
      foreground = tokens.onSelection;
    };
  };
in
{
  inherit tokens fallback pairs;

  # Base16-shaped compatibility data belongs here, not in individual apps.
  # Consumers may use the shape required by upstream templates without
  # becoming independent owners of the underlying Stylix palette.
  base16.withHashtag = colors.withHashtag // {
    scheme = "gjallarOS";

    base00 = "#${fallback.surface}";
    base01 = "#${fallback.surfaceVariant}";
    base02 = "#${fallback.surfaceContainerHigh}";
    base03 = "#${fallback.outlineVariant}";
    base04 = "#${fallback.outline}";
    base05 = "#${fallback.onSurface}";
    base08 = "#${fallback.error}";
    base0D = "#${fallback.primary}";
    base0E = "#${fallback.tertiary}";
  };

  # Stylix supplies the Kvantum artwork, while GjallarOS owns what each
  # Base16-shaped slot means semantically.
  kvantum = {
    roles = {
      base00 = tokens.surface;
      base01 = tokens.surfaceVariant;

      # Base02 represents an elevated/interactive surface.
      base02 = tokens.surfaceContainerHigh;

      base03 = tokens.outlineVariant;
      base04 = tokens.outline;
      base05 = tokens.onSurface;
      base08 = tokens.error;
      base0D = tokens.primary;
      base0E = tokens.tertiary;
    };

    fallback = {
      base00 = colors.base00;
      base01 = colors.base01;
      base02 = colors.base02;
      base03 = colors.base03;
      base04 = colors.base04;
      base05 = colors.base05;
      base08 = colors.base08;
      base0D = colors.base0D;
      base0E = colors.base0E;
    };
  };
}
