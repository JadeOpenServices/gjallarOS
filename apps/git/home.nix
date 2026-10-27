{
  config,
  pkgs,
  lib,
  settings,
  ...
}:

lib.mkIf config.gjallar.apps.git.enable {
  programs.git = {
    enable = true;
    settings = {
      core.editor = lib.getExe pkgs.${settings.preferredEditor};
      user = {
        name = settings.name;
        email = settings.email;
      };
      stash = {
        showPatch = true;
      };
      # GitHub logins go through gh. This file is rewritten every generation,
      # so the gh path here never outlives a garbage collection the way the
      # one `gh auth setup-git` writes into ~/.gitconfig does.
      credential = lib.genAttrs [ "https://github.com" "https://gist.github.com" ] (_: {
        helper = [
          ""
          "!${lib.getExe pkgs.gh} auth git-credential"
        ];
      });
    };
  };

  # Old `gh auth setup-git` runs left their store path in ~/.gitconfig, which
  # git reads after the file above. Only those helper lines are rewritten.
  home.activation.gitStableGhHelper = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    cfg="$HOME/.gitconfig"
    if [ -f "$cfg" ] && [ ! -L "$cfg" ] \
      && ${pkgs.gnugrep}/bin/grep -q -E '^[[:space:]]*helper = !/nix/store/[^ ]+/bin/\.?gh(-wrapped)? auth git-credential[[:space:]]*$' "$cfg"; then
      run ${pkgs.gnused}/bin/sed -i -E \
        's|^([[:space:]]*helper = !)/nix/store/[^ ]+/bin/\.?gh(-wrapped)? (auth git-credential)[[:space:]]*$|\1/run/current-system/sw/bin/gh \3|' \
        "$cfg"
    fi
  '';

  programs.lazygit = {
    enable = true;
    settings = {
      gui = {
        switchTabsWithPanelJumpKeys = true;
      };
    };
  };
}
