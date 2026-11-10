{
  config,
  settings,
  lib,
  ...
}:
let
  details = settings.themeDetails;
  semanticTheme = import ./semantic.nix { inherit config; };
in
{
  home.pointerCursor.enable = true;

  # OpenJade shape rule (6 px windows and cards, 4 px controls, 2 px marks,
  # square bars, a 1 px line instead of shadows). Stylix writes one gtk.css
  # for both GTK versions, and the rule uses GTK4 / libadwaita syntax, so
  # the @import in stylix.targets.gtk.extraCss below finds the real file
  # only under gtk-4.0. GTK3 gets an empty file and keeps adw-gtk3's shape.
  xdg.configFile."gtk-4.0/openjade-shape.css".source = ./openjade-shape.css;
  xdg.configFile."gtk-3.0/openjade-shape.css".text = ''
    /* No GTK3 shape rule yet. GTK4 gets the OpenJade rule from the
       file of the same name in gtk-4.0. */
  '';

  xdg.userDirs.setSessionVariables = true;

  gtk = {
    enable = true;
    iconTheme = {
      name = lib.mkForce details.icons;
      package = lib.mkForce details.iconsPkg;
    };

  };

  stylix = {
    targets.gtk.extraCss = ''
      @define-color theme_selected_bg_color #${semanticTheme.fallback.selection};
      @define-color theme_selected_fg_color #${semanticTheme.fallback.onSelection};

      @define-color theme_bg_color #${semanticTheme.fallback.surface};
      @define-color theme_fg_color #${semanticTheme.fallback.onSurface};
      @define-color theme_base_color #${semanticTheme.fallback.surface};
      @define-color theme_text_color #${semanticTheme.fallback.onSurface};
      @define-color window_bg_color #${semanticTheme.fallback.surface};
      @define-color window_fg_color #${semanticTheme.fallback.onSurface};
      @define-color view_bg_color #${semanticTheme.fallback.surface};
      @define-color view_fg_color #${semanticTheme.fallback.onSurface};
      @define-color headerbar_bg_color #${semanticTheme.fallback.surfaceVariant};
      @define-color headerbar_fg_color #${semanticTheme.fallback.onSurfaceVariant};
      @define-color popover_bg_color #${semanticTheme.fallback.surfaceVariant};
      @define-color popover_fg_color #${semanticTheme.fallback.onSurfaceVariant};
      @define-color card_bg_color #${semanticTheme.fallback.surfaceVariant};
      @define-color card_fg_color #${semanticTheme.fallback.onSurfaceVariant};
      selection,
      entry selection,
      textview text selection,
      treeview.view:selected,
      row:selected,
      list row:selected {
        background-color: #${semanticTheme.fallback.selection};
        color: #${semanticTheme.fallback.onSelection};
      }

      *:focus-visible {
        outline-color: #${semanticTheme.fallback.selection};
      }

      @import url("openjade-shape.css");
    '';

    targets.blender.enable = false;
    targets.forge.enable = false;
    targets.gdu.enable = false;
    targets.vencord.enable = false;
    targets.gedit.enable = false;
    targets.gnome-text-editor.enable = false;
    targets.gtksourceview.enable = false;
    targets.ghostty.enable = false;
    targets.tmux.enable = false;
    targets.hyprlock.enable = false;
    targets.hyprland.enable = false;
    targets.btop.enable = false;
  };
}
