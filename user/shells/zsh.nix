{
  config,
  pkgs,
  lib,
  ...
}:

{
  imports = [
    ./lib/bat.nix
    ./lib/starship.nix
    ./lib/tmux
  ];

  programs.tmux.shell = "${pkgs.zsh}/bin/zsh";
  programs.starship.enableZshIntegration = true;

  programs.zsh = {
    enable = true;
    dotDir = "${config.xdg.configHome}/zsh";
    autosuggestion.enable = true;
    enableCompletion = true;
    syntaxHighlighting.enable = true;
    history.size = 100000;
    shellAliases = {
      ls = "exa --color=auto --icons";
      l = "ls -l";
      la = "ls -a";
      lla = "ls -la";
      lt = "ls --tree";
      llat = "ls -al -s time";
      ".." = "cd ..";
      "..." = "cd ../..";
      "...." = "cd ../../../..";
      "....." = "cd ../../../../..";
      "......" = "cd ../../../../../..";
      cat = "bat";
      gs = "git status";
      gd = "git diff";
      gl = "git log --oneline --graph";
      ga = "git add";
      gc = "git commit";
      gca = "git commit -a";
      v = "$EDITOR";
      mv = "mv -v";
      cp = "rsync -avhW --no-compress --progress";
      rm = "rm -rv";
      # The store copy from the built revision: the system checkout belongs
      # to root, and nix refuses another user's git repository. path: makes
      # nix read the store path as a flake, not as a derivation.
      nix-python = "nix develop path:${../../shells/python} -c zsh";
      nix-lampray = "nix develop path:${../../shells/lampray} -c zsh";
      nix-invoke = "nix develop path:${../../shells/invoke} -c zsh";
      nix-comfy = "nix develop path:${../../shells/comfy} -c zsh";
      nix-pkg-build = "nix build -f default.nix --arg pkgs 'import <nixpkgs> {}'";
      nix-pkg-shell = "nix shell -f default.nix --arg pkgs 'import <nixpkgs> {}'";
      git-clean = "git clean -xfd; git submodule foreach git clean -xfd";
      nekoray = "xhost + local:; sudo nekoray";
      docker-run-ssh-agent = "sudo docker run --mount type=bind,source=$SSH_AUTH_SOCK,target=/ssh-agent --env SSH_AUTH_SOCK=/ssh-agent";
      git-rm-deleted-by-us = "git status --short | awk '$1==\"DU\" {print $2}' | xargs git rm";
    };
    initContent = ''
      set -o emacs

      # Pasted scripts of several KB grew the history file to 24 MB, and zsh
      # reads all of it at every start. Larger entries stay in this
      # session's history but are not saved to the file.
      zshaddhistory() { (( ''${#1} <= 4096 )) || return 2; }

      # Authenticate plain sudo through the GjallarOS flow: fingerprint
      # first, then a terminal password prompt. Non-interactive and explicit
      # authentication modes go straight to sudo.
      sudo() {
        case "$1" in
          -n|-S|-A|-k|-K|-h|-V|--non-interactive|--stdin|--askpass|--help|--version) ;;
          *) if (( $+commands[gjallarctl] )) && ! command sudo -n -v 2>/dev/null; then
               command gjallarctl auth || return
             fi ;;
        esac
        command sudo "$@"
      }
      export PATH=$PATH:${config.home.homeDirectory}/.local/bin
    '';
  };

  home.packages = with pkgs; [
    eza
    tldr
  ];
}
