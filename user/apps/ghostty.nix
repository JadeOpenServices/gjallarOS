{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  semanticTheme = import ../../themes/lib/semantic.nix { inherit config; };

  # Run by the login shell itself: fastfetch names its parent process as the
  # shell, and a bash wrapper made it print bash.
  ghosttyShell = pkgs.writeScript "gjallar-ghostty-shell" ''
    #!${lib.getExe pkgs.${settings.shell}}
    if [ -t 1 ]; then
      printf '\n'
      ${lib.getExe pkgs.fastfetch} || true
      printf '\n'
    fi

    exec ${lib.getExe pkgs.${settings.shell}} "$@"
  '';
in
{
  home.activation.gjallarGhosttyNoctaliaFallback = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          theme_dir="$HOME/.config/ghostty/themes"
          theme_file="$theme_dir/noctalia"

          ${pkgs.coreutils}/bin/mkdir -p "$theme_dir"

          if [ ! -e "$theme_file" ]; then
            cat >"$theme_file" <<'THEME'
    background = #${semanticTheme.fallback.terminal.background}
    foreground = #${semanticTheme.fallback.terminal.foreground}
    cursor-color = #${semanticTheme.fallback.terminal.cursor}
    cursor-text = #${semanticTheme.fallback.terminal.cursorText}
    selection-background = #${semanticTheme.fallback.selection}
    selection-foreground = #${semanticTheme.fallback.onSelection}
    palette = 0=#${semanticTheme.fallback.terminal.normal.black}
    palette = 1=#${semanticTheme.fallback.terminal.normal.red}
    palette = 2=#${semanticTheme.fallback.terminal.normal.green}
    palette = 3=#${semanticTheme.fallback.terminal.normal.yellow}
    palette = 4=#${semanticTheme.fallback.terminal.normal.blue}
    palette = 5=#${semanticTheme.fallback.terminal.normal.magenta}
    palette = 6=#${semanticTheme.fallback.terminal.normal.cyan}
    palette = 7=#${semanticTheme.fallback.terminal.normal.white}
    palette = 8=#${semanticTheme.fallback.terminal.bright.black}
    palette = 9=#${semanticTheme.fallback.terminal.bright.red}
    palette = 10=#${semanticTheme.fallback.terminal.bright.green}
    palette = 11=#${semanticTheme.fallback.terminal.bright.yellow}
    palette = 12=#${semanticTheme.fallback.terminal.bright.blue}
    palette = 13=#${semanticTheme.fallback.terminal.bright.magenta}
    palette = 14=#${semanticTheme.fallback.terminal.bright.cyan}
    palette = 15=#${semanticTheme.fallback.terminal.bright.white}
    THEME
          fi
  '';

  programs.ghostty = {
    enable = true;
    package = pkgs.ghostty;
    systemd.enable = true;
    enableZshIntegration = settings.shell == "zsh";

    settings = {
      command = "${ghosttyShell}";

      theme = "noctalia";

      font-family = settings.themeDetails.font;
      font-size = settings.themeDetails.fontSize;

      window-padding-x = 15;
      window-padding-y = 15;
      window-padding-balance = true;
      window-decoration = "none";

      cursor-style = "underline";
      cursor-style-blink = false;

      shell-integration = "detect";
      shell-integration-features = "no-cursor,title";

      confirm-close-surface = true;

      clipboard-read = "ask";
      clipboard-write = "allow";
      clipboard-paste-protection = true;
      clipboard-paste-bracketed-safe = false;
      copy-on-select = false;
      # Shift+Insert pastes the clipboard like in every other app. Ghostty's
      # default reads the primary selection, which paste-once empties.
      keybind = [ "shift+insert=paste_from_clipboard" ];

      title-report = false;
      scrollback-limit = 10485760;
    };
  };
}
