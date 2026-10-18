{
  pkgs,
  lib,
  settings,
  config,
  ...
}:
let
  details = settings.themeDetails;
  # The session's Noctalia colors as gjallarctl checked them:
  # base00=rrggbb,...,base0F=rrggbb. The evaluation reads no file for them,
  # and anything else stops it.
  noctaliaColors = builtins.getEnv "GJALLAR_NOCTALIA_COLORS";
  noctaliaKeys = map (digit: "base0${digit}") (lib.stringToCharacters "0123456789ABCDEF");
  noctaliaFields = map (lib.match "(base0[0-9A-F])=([0-9a-f]{6})") (
    lib.splitString "," noctaliaColors
  );
  noctaliaPalette =
    if noctaliaColors == "" then
      { }
    else if
      builtins.all (field: field != null) noctaliaFields
      && map builtins.head noctaliaFields == noctaliaKeys
    then
      builtins.listToAttrs (
        map (field: lib.nameValuePair (builtins.head field) (builtins.elemAt field 1)) noctaliaFields
      )
    else
      throw "GJALLAR_NOCTALIA_COLORS is not base00=rrggbb,...,base0F=rrggbb";
in
{
  stylix = {
    enable = true;
    polarity = "dark";
    base16Scheme = lib.mkIf (
      details.themeName != null
    ) "${pkgs.base16-schemes}/share/themes/${details.themeName}.yaml";
    override =
      let
        configured = if details.override == null then { } else details.override;
      in
      lib.mkIf (configured != { } || noctaliaPalette != { }) (configured // noctaliaPalette);
    opacity = {
      terminal = details.opacity;
      applications = details.opacity;
      desktop = details.opacity;
      popups = details.opacity;
    };

    cursor = {
      size = 32;
      name = "phinger-cursors-light";
      package = pkgs.phinger-cursors;
    };

    fonts = {
      sansSerif = {
        package = details.fontPkg;
        name = details.font;
      };
      serif = config.stylix.fonts.sansSerif;
      monospace = config.stylix.fonts.sansSerif;
      emoji = config.stylix.fonts.sansSerif;
    };
  };
}
