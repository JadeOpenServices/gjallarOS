{
  config,
  lib,
  pkgs,
  settings,
  osConfig ? { },
  ...
}:

let
  # The system side owns the AI backend; standalone Home Manager has none.
  aiEnabled = osConfig.gjallar.apps.ai.enable or false;

  aiModel = "gjallaros-caveman-ai";
  agentMode = if settings ? aiAgentMode then settings.aiAgentMode else "workspace";
  ownerMode = agentMode != "workspace";
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };


  gjallarAi = pkgs.writeShellScriptBin "gjallar-ai" ''
    set -euo pipefail

    die() {
      printf 'gjallar-ai: %s\n' "$*" >&2
      exit 1
    }

    for command in \
      git \
      realpath \
      systemctl \
      systemd-escape \
      tmux
    do
      command -v "$command" >/dev/null 2>&1 ||
        die "required command not found: $command"
    done

    mode="cwd"
    workspace=""

    case "''${1-}" in
      --gjallaros)
        mode="gjallaros"
        shift
        ;;

      "")
        ;;

      *)
        mode="explicit"
        workspace="$1"
        shift
        ;;
    esac

    case "$mode" in
      cwd)
        repo="$(
          git rev-parse \
            --show-toplevel \
            2>/dev/null ||
          true
        )"

        [ -n "$repo" ] ||
          die "Start gjallar-ai inside a Git repository."
        ;;

      explicit)
        repo="$(
          git -C "$workspace" \
            rev-parse \
            --show-toplevel \
            2>/dev/null ||
          true
        )"

        [ -n "$repo" ] ||
          die "workspace is not a Git repository"
        ;;

      gjallaros)
        if [ -n "''${GJALLAROS_REPO:-}" ]; then
          repo="$(realpath "''${GJALLAROS_REPO}")"

          [ -f "$repo/.gjallaros-root" ] ||
            die "GJALLAROS_REPO is not a gjallarOS repository"
        else
          current="$(
            git rev-parse \
              --show-toplevel \
              2>/dev/null ||
            true
          )"

          if [ -n "$current" ] && [ -f "$current/.gjallaros-root" ]; then
            repo="$current"
          else
            found=""

            while IFS= read -r -d "" identity
            do
              candidate="$(
                realpath "$(
                  ${pkgs.coreutils}/bin/dirname "$identity"
                )"
              )"

              top="$(
                git -C "$candidate" \
                  rev-parse \
                  --show-toplevel \
                  2>/dev/null ||
                true
              )"

              [ "$top" = "$candidate" ] ||
                continue

              [ -f "$candidate/flake.nix" ] ||
                continue
              [ -f "$candidate/apps/ai/nixos.nix" ] ||
                continue
              [ -f "$candidate/apps/opencode/home.nix" ] ||
                continue

              if [ -n "$found" ] && [ "$found" != "$candidate" ]; then
                die "multiple gjallarOS repositories found; set GJALLAROS_REPO"
              fi

              found="$candidate"
            done < <(
              ${pkgs.findutils}/bin/find \
                "$HOME" \
                -xdev \
                -type f \
                -name .gjallaros-root \
                -print0 \
                2>/dev/null
            )

            [ -n "$found" ] ||
              die "gjallarOS repository not found below HOME; set GJALLAROS_REPO"

            repo="$found"
          fi
        fi
        ;;
    esac

    repo="$(realpath "$repo")"

    top="$(
      git -C "$repo" \
        rev-parse \
        --show-toplevel \
        2>/dev/null ||
      true
    )"

    [ "$top" = "$repo" ] ||
      die "resolved workspace is not a Git repository root"

    escaped="$(
      systemd-escape \
        --path \
        "$repo"
    )"

    [ -n "$escaped" ] ||
      die "failed to encode workspace"

    unit="ai-session@$escaped.service"

    socket="/run/gjallar-ai-session-$escaped/tmux.sock"

    cleanup() {
      systemctl stop "$unit" >/dev/null 2>&1 || true
      : # session owns its own shutdown; no privileged stop on launcher exit
    }
    trap cleanup EXIT INT TERM HUP

    printf \
      '\r\033[2K◐  gjallarCode · Starting local AI…'

printf '\r\033[2K🔐  gjallarCode · Authentication\n'
        sudo -k

        sudo \
          /run/current-system/sw/bin/gjallar-ai-session-start \
          "$unit" ||
      die "failed to start controlled AI system session"

    # ai-session@ is Type=notify: the start above returns once tmux runs.
    if [ ! -S "$socket" ]; then
      systemctl status \
        "$unit" \
        --no-pager \
        >&2 ||
        true

      die "controlled AI console did not become ready"
    fi

    printf \
      '\r\033[2K✓  gjallarCode · Local AI ready\n'

    set +e

    ${pkgs.tmux}/bin/tmux \
      -S "$socket" \
      attach-session \
      -t gjallar-ai

    rc=$?

    set -e

    exit "$rc"
  '';


  gjallarAiDesktop = pkgs.makeDesktopItem {
    name = "gjallarOS-ai";
    desktopName = "gjallarOS AI";
    comment = "Secure local gjallarCode Caveman agent";
    exec =
      "${gjallarAi}/bin/gjallar-ai --gjallaros";
    icon = "${pkgs.papirus-icon-theme}/share/icons/Papirus/64x64/apps/devassistant.svg";
    terminal = true;
    categories = [ "Development" ];
  };

in
lib.mkIf (config.gjallar.apps.opencode.enable && aiEnabled) {


  programs.opencode = {
    enable = true;
  };

  home.packages = [
    pkgs.tmux
    gjallarAi
    gjallarAiDesktop
    (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-ai" (builtins.readFile ./_gjallar-ai))

    (pkgs.writeShellScriptBin "gjallar-agent-tool" ''
      set -euo pipefail
      workspace="$(git rev-parse --show-toplevel 2>/dev/null)" || {
        echo "gjallar-agent-tool: start inside a Git workspace" >&2; exit 2;
      }
      exec ${gjallarctl}/bin/gjallarctl ai tool \
        --workspace "$workspace" \
        --mode ${lib.escapeShellArg agentMode} \
        --approval "''${XDG_RUNTIME_DIR}/gjallar-ai/approval.json" \
        --audit "''${XDG_STATE_HOME:-$HOME/.local/state}/gjallar-ai/audit.jsonl" \
        "$@"
    '')

    (pkgs.writeShellScriptBin "opencode-local" ''
      set -euo pipefail

      unset OPENAI_BASE_URL OPENAI_API_KEY

      unset         ANTHROPIC_API_KEY         ANTHROPIC_AUTH_TOKEN         GOOGLE_API_KEY         OPENROUTER_API_KEY

      exec ${gjallarAi}/bin/gjallar-ai "$@"
    '')

    (pkgs.writeShellScriptBin "gjallar-ai-approve" ''
      set -euo pipefail
      workspace="$(git rev-parse --show-toplevel 2>/dev/null)" || {
        echo "gjallar-ai-approve: start inside the target Git workspace" >&2; exit 2;
      }
      runtime="''${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
      exec ${gjallarctl}/bin/gjallarctl ai approve \
        --workspace "$workspace" --grant "$runtime/gjallar-ai/approval.json" "$@"
    '')

    (pkgs.writeShellScriptBin "gjallar-research" ''
      set -euo pipefail

      url="''${1:-}"
      [[ "$url" =~ ^https:// ]] || {
        echo "Usage: gjallar-research https://approved-host/path" >&2
        exit 2
      }

      exec ${pkgs.curl}/bin/curl \
        --fail \
        --silent \
        --show-error \
        --unix-socket /run/gjallar-ai/research.sock \
        --max-time 15 \
        --get --data-urlencode "url=$url" \
        http://localhost/research
    '')
  ];
}
