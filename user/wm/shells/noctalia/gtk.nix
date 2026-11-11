{
  config,
  lib,
  pkgs,
  ...
}:

let
  semanticTheme = import ../../../../themes/lib/semantic.nix { inherit config; };
  contrastGuard = import ../../../../themes/lib/contrast.nix { inherit pkgs; };

  template = ''
    @define-color theme_bg_color #{{colors.${semanticTheme.tokens.surface}.default.hex_stripped}};
    @define-color theme_fg_color #{{colors.${semanticTheme.tokens.onSurface}.default.hex_stripped}};
    @define-color theme_base_color #{{colors.${semanticTheme.tokens.surface}.default.hex_stripped}};
    @define-color theme_text_color #{{colors.${semanticTheme.tokens.onSurface}.default.hex_stripped}};

    @define-color window_bg_color #{{colors.${semanticTheme.tokens.surface}.default.hex_stripped}};
    @define-color window_fg_color #{{colors.${semanticTheme.tokens.onSurface}.default.hex_stripped}};
    @define-color view_bg_color #{{colors.${semanticTheme.tokens.surface}.default.hex_stripped}};
    @define-color view_fg_color #{{colors.${semanticTheme.tokens.onSurface}.default.hex_stripped}};

    @define-color headerbar_bg_color #{{colors.${semanticTheme.tokens.surfaceVariant}.default.hex_stripped}};
    @define-color headerbar_fg_color #{{colors.${semanticTheme.tokens.onSurfaceVariant}.default.hex_stripped}};
    @define-color popover_bg_color #{{colors.${semanticTheme.tokens.surfaceVariant}.default.hex_stripped}};
    @define-color popover_fg_color #{{colors.${semanticTheme.tokens.onSurfaceVariant}.default.hex_stripped}};
    @define-color card_bg_color #{{colors.${semanticTheme.tokens.surfaceVariant}.default.hex_stripped}};
    @define-color card_fg_color #{{colors.${semanticTheme.tokens.onSurfaceVariant}.default.hex_stripped}};

    @define-color theme_selected_bg_color #{{colors.${semanticTheme.tokens.selection}.default.hex_stripped}};
    @define-color theme_selected_fg_color #{{colors.${semanticTheme.tokens.onSelection}.default.hex_stripped}};
    @define-color accent_bg_color #{{colors.${semanticTheme.tokens.selection}.default.hex_stripped}};
    @define-color accent_fg_color #{{colors.${semanticTheme.tokens.onSelection}.default.hex_stripped}};
  '';

  fallback =
    builtins.replaceStrings
      [
        "{{colors.${semanticTheme.tokens.surface}.default.hex_stripped}}"
        "{{colors.${semanticTheme.tokens.onSurface}.default.hex_stripped}}"
        "{{colors.${semanticTheme.tokens.surfaceVariant}.default.hex_stripped}}"
        "{{colors.${semanticTheme.tokens.onSurfaceVariant}.default.hex_stripped}}"
        "{{colors.${semanticTheme.tokens.selection}.default.hex_stripped}}"
        "{{colors.${semanticTheme.tokens.onSelection}.default.hex_stripped}}"
      ]
      [
        semanticTheme.fallback.surface
        semanticTheme.fallback.onSurface
        semanticTheme.fallback.surfaceVariant
        semanticTheme.fallback.onSurfaceVariant
        semanticTheme.fallback.selection
        semanticTheme.fallback.onSelection
      ]
      template;

  fallbackFile = pkgs.writeText "noctalia-gtk.css" fallback;
in
{
  gtk = {
    colorScheme = lib.mkForce "dark";


    gtk3 = {
      colorScheme = lib.mkForce "dark";
      theme = lib.mkForce {
        name = "adw-gtk3-dark";
        package = pkgs.adw-gtk3;
      };

      extraConfig."gtk-application-prefer-dark-theme" = 1;
    };

    # GTK 4 takes the colour scheme from the Settings portal, which reads
    # the dconf color-scheme below. Nothing goes into gtk-4.0/settings.ini:
    # Home Manager 26.05 writes gtk-interface-color-scheme as a number GTK
    # cannot read, and libadwaita warns about
    # gtk-application-prefer-dark-theme (Plane 326, upstream queue 753).
    gtk4.colorScheme = lib.mkForce null;
  };

  dconf.settings."org/gnome/desktop/interface".color-scheme = "prefer-dark";

  stylix.targets.gtk.extraCss = lib.mkAfter ''
    @import url("noctalia.css");
  '';

  programs.noctalia.settings.theme.templates.user = {
    gtk3 = {
      input_path = "$XDG_CONFIG_HOME/noctalia/templates/gtk.css";
      output_path = "$XDG_CONFIG_HOME/gtk-3.0/noctalia.css";
      pre_hook = contrastGuard.preHook;
    };

    gtk4 = {
      input_path = "$XDG_CONFIG_HOME/noctalia/templates/gtk.css";
      output_path = "$XDG_CONFIG_HOME/gtk-4.0/noctalia.css";
      pre_hook = contrastGuard.preHook;
    };
  };

  xdg.configFile."noctalia/templates/gtk.css".text = template;

  home.activation.noctaliaGtkFallback = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    for target in \
      "$HOME/.config/gtk-3.0/noctalia.css" \
      "$HOME/.config/gtk-4.0/noctalia.css"
    do
      if [ ! -e "$target" ]; then
        run ${pkgs.coreutils}/bin/install \
          -D -m 0600 \
          ${fallbackFile} \
          "$target"
      fi
    done
  '';
}
