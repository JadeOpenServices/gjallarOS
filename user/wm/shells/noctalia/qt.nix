{
  config,
  inputs,
  lib,
  pkgs,
  ...
}:
let
  semanticTheme = import ../../../../themes/lib/semantic.nix { inherit config; };
  contrastGuard = import ../../../../themes/lib/contrast.nix { inherit pkgs; };

  # Reuse Stylix's widget artwork; Noctalia supplies the changing palette.
  roles = semanticTheme.kvantum.roles;
  keys = builtins.attrNames roles;
  tokens = map (key: "{{${key}-hex}}") keys;
  themeFiles = {
    kvconfig = builtins.readFile "${inputs.stylix}/modules/qt/kvconfig.mustache";
    svg = builtins.readFile "${inputs.stylix}/modules/qt/kvantum.svg.mustache";
  };

  # KDE Framework consumers such as Dolphin use KColorScheme in addition to
  # the Qt widget style. Keep those colors under the same Noctalia semantic
  # authority instead of allowing KDE's fallback palette to diverge.
  kdeRoles = {
    surface = "surface";
    surfaceVariant = "surfaceVariant";
    onSurface = "onSurface";
    onSurfaceVariant = "onSurfaceVariant";
    primary = "primary";
    onPrimary = "onPrimary";
    secondary = "secondary";
    tertiary = "tertiary";
    error = "error";
    outlineVariant = "outlineVariant";
  };
  kdeKeys = builtins.attrNames kdeRoles;
  kdeTokens = map (key: "{{kde-${key}-hex}}") kdeKeys;

  # Follow the same KColorScheme group structure Stylix uses for KDE, but
  # consume gjallarOS semantic roles so Noctalia remains the live owner.
  kdeStandardColors = {
    BackgroundAlternate = "#{{kde-surfaceVariant-hex}}";
    BackgroundNormal = "#{{kde-surface-hex}}";
    DecorationFocus = "#{{kde-primary-hex}}";
    DecorationHover = "#{{kde-primary-hex}}";
    ForegroundActive = "#{{kde-onSurface-hex}}";
    ForegroundInactive = "#{{kde-onSurfaceVariant-hex}}";
    ForegroundLink = "#{{kde-primary-hex}}";
    ForegroundNegative = "#{{kde-error-hex}}";
    ForegroundNeutral = "#{{kde-secondary-hex}}";
    ForegroundNormal = "#{{kde-onSurface-hex}}";
    ForegroundPositive = "#{{kde-tertiary-hex}}";
    ForegroundVisited = "#{{kde-tertiary-hex}}";
  };

  kdeTemplate = lib.generators.toINI { } (
    {
      General = {
        ColorScheme = "Noctalia";
        Name = "Noctalia";
      };

      "ColorEffects:Disabled" = {
        ColorAmount = 0;
        ColorEffect = 0;
        ContrastAmount = 0.5;
        ContrastEffect = 1;
        IntensityAmount = 0;
        IntensityEffect = 0;
      };

      "ColorEffects:Inactive" = {
        ColorAmount = 0;
        ColorEffect = 0;
        ContrastAmount = 0.5;
        ContrastEffect = 1;
        IntensityAmount = 0;
        IntensityEffect = 0;
      };
    }
    // lib.genAttrs
      (map (name: "Colors:${name}") [
        "Window"
        "View"
        "Button"
        "Tooltip"
        "Complementary"
      ])
      (_: kdeStandardColors)
    // {
      "Colors:Selection" = {
        BackgroundAlternate = "#{{kde-primary-hex}}";
        BackgroundNormal = "#{{kde-primary-hex}}";
        DecorationFocus = "#{{kde-onPrimary-hex}}";
        DecorationHover = "#{{kde-onPrimary-hex}}";
        ForegroundActive = "#{{kde-onPrimary-hex}}";
        ForegroundInactive = "#{{kde-onPrimary-hex}}";
        ForegroundLink = "#{{kde-onPrimary-hex}}";
        ForegroundNegative = "#{{kde-onPrimary-hex}}";
        ForegroundNeutral = "#{{kde-onPrimary-hex}}";
        ForegroundNormal = "#{{kde-onPrimary-hex}}";
        ForegroundPositive = "#{{kde-onPrimary-hex}}";
        ForegroundVisited = "#{{kde-onPrimary-hex}}";
      };

      WM = {
        activeBackground = "#{{kde-surface-hex}}";
        activeBlend = "#{{kde-secondary-hex}}";
        activeForeground = "#{{kde-onSurface-hex}}";
        inactiveBackground = "#{{kde-surface-hex}}";
        inactiveBlend = "#{{kde-outlineVariant-hex}}";
        inactiveForeground = "#{{kde-onSurfaceVariant-hex}}";
      };
    }
  );

  renderTemplate = builtins.replaceStrings tokens (
    map (key: "{{colors.${roles.${key}}.default.hex_stripped}}") keys
  );
  renderFallback = builtins.replaceStrings tokens (
    map (key: builtins.getAttr key semanticTheme.kvantum.fallback) keys
  );

  renderKdeTemplate = builtins.replaceStrings kdeTokens (
    map (
      key:
      let
        semanticKey = kdeRoles.${key};
        token = semanticTheme.tokens.${semanticKey};
      in
      "{{colors.${token}.default.hex_stripped}}"
    ) kdeKeys
  ) kdeTemplate;

  renderKdeFallback = builtins.replaceStrings kdeTokens (
    map (
      key:
      let
        semanticKey = kdeRoles.${key};
      in
      semanticTheme.fallback.${semanticKey}
    ) kdeKeys
  ) kdeTemplate;

  kdeFallback = pkgs.writeText "noctalia-kde.colors" renderKdeFallback;
in
{
  qt = {
    enable = true;
    platformTheme.name = lib.mkForce "qtct";
    style.name = lib.mkForce "kvantum";
    kvantum.settings.General.theme = lib.mkForce "Noctalia";

    # qt6ct's KDE integration loads this through KColorScheme and exports the
    # selected path to KDE Framework consumers inside each Qt application.
    qt5ctSettings.Appearance.color_scheme_path =
      lib.mkForce "${config.xdg.dataHome}/color-schemes/Noctalia.colors";
    qt6ctSettings.Appearance.color_scheme_path =
      lib.mkForce "${config.xdg.dataHome}/color-schemes/Noctalia.colors";
  };

  programs.noctalia.settings.theme.templates.user =
    (lib.mapAttrs' (
      extension: _:
      lib.nameValuePair "qt_kvantum_${extension}" {
        input_path = "$XDG_CONFIG_HOME/noctalia/templates/qt.${extension}";
        output_path = "$XDG_CONFIG_HOME/Kvantum/Noctalia/Noctalia.${extension}";
        pre_hook = contrastGuard.preHook;
      }
    ) themeFiles)
    // {
      qt_kde_colors = {
        input_path = "$XDG_CONFIG_HOME/noctalia/templates/kde.colors";
        output_path = "$XDG_DATA_HOME/color-schemes/Noctalia.colors";
        pre_hook = contrastGuard.preHook;
      };
    };

  xdg.configFile =
    (lib.mapAttrs' (
      extension: content:
      lib.nameValuePair "noctalia/templates/qt.${extension}" {
        text = renderTemplate content;
      }
    ) themeFiles)
    // {
      "noctalia/templates/kde.colors".text = renderKdeTemplate;
    };

  # Writable outputs belong to Noctalia, with a first-login fallback palette.
  home.activation.noctaliaQtFallback = lib.hm.dag.entryAfter [ "writeBoundary" ] (
    lib.concatStringsSep "\n" (
      lib.mapAttrsToList (
        extension: content:
        let
          target = "${config.xdg.configHome}/Kvantum/Noctalia/Noctalia.${extension}";
          fallback = pkgs.writeText "noctalia-qt.${extension}" (renderFallback content);
        in
        ''
          if [ ! -e ${lib.escapeShellArg target} ]; then
            run ${pkgs.coreutils}/bin/install -D -m 0600 ${fallback} ${lib.escapeShellArg target}
          fi
        ''
      ) themeFiles
    )
  );

  # Noctalia owns this file after login. Seed a semantic fallback only when no
  # live palette exists yet, matching the existing writable Kvantum policy.
  home.activation.noctaliaKdeFallback = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    kde_colors=${"${config.xdg.dataHome}/color-schemes/Noctalia.colors"}
    if [ ! -e "$kde_colors" ]; then
      run ${pkgs.coreutils}/bin/install -D -m 0600 ${kdeFallback} "$kde_colors"
    fi
  '';
}
