{ pkgs }:

{
  # Dolphin uses Darkly for widget rendering only. qtct/Noctalia remains the
  # palette authority, so live semantic colors are not duplicated here.
  styleName = "Darkly";
  package = pkgs.darkly;

  # Keep the plugin lookup version-aware through Nixpkgs' Qt metadata instead
  # of hardcoding lib/qt-6/plugins.
  qtPluginPath = "${pkgs.darkly}/${pkgs.qt6.qtbase.qtPluginPrefix}";

  # OpenJade shape rule for Darkly (keys from darkly.kcfg, v0.5.32). Darkly
  # has one radius for controls and frames, so it takes the 4 px control
  # value. Menus and popups draw no drop shadow.
  config = {
    Common = {
      CornerRadius = 4;
      ShadowSize = "ShadowNone";
    };
    Style = {
      WidgetDrawShadow = false;
      WidgetToolBarShadow = false;
    };
  };
}
