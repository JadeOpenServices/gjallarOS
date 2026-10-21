{
  config,
  inputs,
  lib,
  settings,
  pkgs,
  ...
}:

let
  graphics = import (inputs.oddc + "/nixos/lib/graphics.nix") { inherit lib; } config.oddc.resolved;

  cavemanSrc = pkgs.fetchFromGitHub {
    owner = "JuliusBrussee";
    repo = "caveman";
    rev = "v2.2.0";
    hash = "sha256-wtGKsKtjPL1nG0LDH4vmfMy/nnuQaY6jvNhlwxhEm2E=";
  };

  gjallarCodeOpencode = import ./opencode.nix { inherit pkgs; };

  gjallarClipboardServer = pkgs.writeShellScript "gjallar-ai-clipboard-server" ''
            set -euo pipefail

            uid="$(${pkgs.coreutils}/bin/id -u ${lib.escapeShellArg settings.username})"
            runtime="/run/user/$uid"

            export XDG_RUNTIME_DIR="$runtime"

            exec ${pkgs.python3}/bin/python3 - <<'PYCLIP'
    import os
    import socket
    import struct
    import subprocess
    from pathlib import Path

    UID = os.getuid()

    SOCKET = Path(
        "/run/gjallar-ai-clipboard/socket"
    )

    WAYLAND_RUNTIME = Path(
        f"/run/user/{UID}"
    )

    WL_COPY = "${pkgs.wl-clipboard}/bin/wl-copy"

    MAX_BYTES = 8 * 1024 * 1024

    CGROUP_TOKEN = "/ai-session@"


    def find_wayland():
        configured = os.environ.get(
            "WAYLAND_DISPLAY",
            "",
        )

        if configured:
            candidate = WAYLAND_RUNTIME / configured

            if candidate.is_socket():
                return configured

        for candidate in sorted(
            WAYLAND_RUNTIME.glob("wayland-*")
        ):
            try:
                if candidate.is_socket():
                    return candidate.name
            except OSError:
                continue

        raise RuntimeError(
            "Wayland socket unavailable"
        )


    def authorized(conn):
        raw = conn.getsockopt(
            socket.SOL_SOCKET,
            socket.SO_PEERCRED,
            struct.calcsize("3i"),
        )

        pid, uid, _gid = struct.unpack(
            "3i",
            raw,
        )

        if pid <= 1 or uid != UID:
            return False

        try:
            cgroup = Path(
                f"/proc/{pid}/cgroup"
            ).read_text()
        except OSError:
            return False

        return CGROUP_TOKEN in cgroup


    def receive(conn):
        data = bytearray()

        while True:
            chunk = conn.recv(65536)

            if not chunk:
                break

            data.extend(chunk)

            if len(data) > MAX_BYTES:
                raise RuntimeError(
                    "clipboard payload exceeds 8 MiB"
                )

        return bytes(data)


    SOCKET.parent.mkdir(
        mode=0o700,
        parents=True,
        exist_ok=True,
    )

    try:
        SOCKET.unlink()
    except FileNotFoundError:
        pass

    server = socket.socket(
        socket.AF_UNIX,
        socket.SOCK_STREAM,
    )

    server.bind(str(SOCKET))

    os.chmod(
        SOCKET,
        0o600,
    )

    server.listen(8)

    while True:
        conn, _ = server.accept()

        with conn:
            if not authorized(conn):
                continue

            try:
                payload = receive(conn)

                display = find_wayland()

                env = os.environ.copy()

                env["XDG_RUNTIME_DIR"] = str(
                    WAYLAND_RUNTIME
                )

                env["WAYLAND_DISPLAY"] = display

                subprocess.run(
                    [WL_COPY],
                    input=payload,
                    env=env,
                    check=True,
                )

            except Exception:
                continue
    PYCLIP
  '';

  gjallarWlCopy = pkgs.writeShellScriptBin "wl-copy" ''
    set -euo pipefail

    if [ -z "''${TMUX:-}" ]; then
      echo \
        "gjallarCode clipboard: TMUX unavailable" \
        >&2
      exit 1
    fi

    ${pkgs.tmux}/bin/tmux \
      set-option \
      -g \
      set-clipboard \
      external \
      >/dev/null

    ${pkgs.tmux}/bin/tmux \
      set-option \
      -as \
      terminal-features \
      ',xterm-ghostty:clipboard' \
      >/dev/null 2>&1 || true

    exec ${pkgs.tmux}/bin/tmux \
      load-buffer \
      -w \
      -

  '';

  aiSystemPrompt = (builtins.fromJSON (builtins.readFile ./system-prompt.json)).system;

  # ROCm compute on an AMD iGPU shares the GPU with the compositor; a hang or
  # reset there takes Hyprland down. Vulkan (RADV) coexists with the desktop.
  ollamaPackage =
    if
      graphics.vendor == "amd" && graphics.type == "integrated" && builtins.hasAttr "ollama-vulkan" pkgs
    then
      pkgs.ollama-vulkan
    else if graphics.vendor == "amd" && builtins.hasAttr "ollama-rocm" pkgs then
      pkgs.ollama-rocm
    else
      pkgs.ollama;

  ollamaIgpuEnable =
    lib.optionalAttrs
      (
        config.gjallar.apps.ai.enable
        && graphics.vendor == "amd"
        && graphics.type == "integrated"
        && settings.graphicsIntegratedBusId != ""
      )
      {
        OLLAMA_IGPU_ENABLE = "1";
      };

  ollamaEnvironment = {
    OLLAMA_NUM_PARALLEL = "1";
    OLLAMA_MAX_LOADED_MODELS = "1";
    OLLAMA_KEEP_ALIVE = "5m";
    OLLAMA_CONTEXT_LENGTH = toString settings.aiContextTokens;
    OLLAMA_FLASH_ATTENTION = "1";
    OLLAMA_KV_CACHE_TYPE = "q8_0";
  }
  // ollamaIgpuEnable;

  securityInstructions = builtins.fromJSON (builtins.readFile ./security-instructions.json);

  # A central server (aiEndpoint) replaces the on-device ollama: nothing is
  # downloaded or run locally, and the server's hardware sets model and context.
  remote = (settings.aiEndpoint or "") != "";
  upstream = if remote then settings.aiEndpoint else "http://127.0.0.1:11434";
  baseModel = if remote then settings.aiRemoteModel else settings.aiModel;
  contextTokens = if remote then settings.aiRemoteContextTokens else settings.aiContextTokens;

  # The system logs in to a central server with a bearer token. It never
  # enters the store: gjallarctl leaves it root-only at tokenPending and
  # ai-endpoint-token-seal encrypts it with systemd-creds into tokenSealed.
  tokenPending = "/var/lib/gjallarOS/ai/endpoint-token";
  tokenSealed = "/var/lib/gjallarOS/ai/endpoint-token.cred";
  tokenCredential = "ai-endpoint-token";
  # A central server is created through the API: the ollama CLI cannot send a
  # token.
  remoteCreateRequest = pkgs.writeText "gjallaros-caveman-ai-create.json" (
    builtins.toJSON {
      model = upstreamModel;
      from = baseModel;
      system = "${aiSystemPrompt}\n\n${securityInstructions.system}";
      parameters.num_ctx = contextTokens;
      stream = false;
    }
  );
  remotePullRequest = pkgs.writeText "gjallaros-caveman-ai-pull.json" (
    builtins.toJSON {
      model = baseModel;
      stream = false;
    }
  );

  assistantModel = "gjallaros-caveman-ai";
  # On a shared server each definition gets its own name, so clients with
  # different prompts or models never overwrite each other.
  upstreamModel =
    if remote then
      "${assistantModel}-${builtins.substring 0 12 modelDefinitionHash}"
    else
      assistantModel;
  modelDefinition = ''
    FROM ${baseModel}
    ${lib.optionalString (
      !remote && accelerationProfile.numGpu != null
    ) "PARAMETER num_gpu ${toString accelerationProfile.numGpu}"}
    SYSTEM """${aiSystemPrompt}

    ${securityInstructions.system}"""
    PARAMETER num_ctx ${toString contextTokens}
  '';
  accelerationProfileName = settings.aiAccelerationProfile;

  accelerationProfiles = {
    auto = {
      numGpu = null;
      description = "Ollama automatic GPU placement";
    };

    full = {
      numGpu = 999;
      description = "Request complete model-layer GPU offload";
    };

    cpu-safe = {
      numGpu = 0;
      description = "CPU-only model execution";
    };
  };

  accelerationProfile =
    accelerationProfiles.${accelerationProfileName}
      or (throw "Unknown Ollama acceleration profile: ${accelerationProfileName}");

  modelDefinitionFile = pkgs.writeText "gjallaros-caveman-ai.Modelfile" modelDefinition;
  modelDefinitionHash = builtins.hashString "sha256" modelDefinition;

  agentMode = if settings ? aiAgentMode then settings.aiAgentMode else "workspace";

  managedOpencodeConfig = pkgs.writeText "gjallar-opencode-managed.json" ''
    {
      "$schema": "https://opencode.ai/config.json",

      "autoupdate": false,
      "share": "disabled",

      "provider": {
        "ollama": {
          "npm": "@ai-sdk/openai-compatible",
          "name": "GjallarOS Model Broker",

          "options": {
            "baseURL": "http://127.0.0.1:11434/v1"
          },

          "models": {
            "${assistantModel}": {
              "name": "GjallarOS Local ${assistantModel}"
            }
          }
        }
      },

      "model": "ollama/${assistantModel}",

      "default_agent": "build",


      "permission": {
        "*": "deny",

        "read": {
          "*": "allow",
          "*.env": "deny",
          "*.env.*": "deny",
          "**/.ssh/**": "deny",
          "**/.gnupg/**": "deny",
          "**/.password-store/**": "deny",
          "**/.thunderbird/**": "deny"
        },

        "glob": "allow",
        "grep": "allow",
        "list": "allow",
        "edit": "allow",

        "todowrite": "allow",
        "lsp": "allow",
        "question": "allow",
        "doom_loop": "deny",
        "skill": {
          "*": "deny",
          "gjallaros": "allow"
        },


        "external_directory": "deny",
        "webfetch": "deny",
        "websearch": "deny",
        "task": "deny",

        "bash": {
          "*": "deny",
          "gjallar-agent-tool *": "allow",
          "gjallar-research *": "allow"
        }
      },



      "agent": {
        "gjallar": {
          "description": "GjallarOS local engineering agent",
          "mode": "primary",
          "model": "ollama/${assistantModel}",
          "steps": 100,

          "permission": {
            "bash": {
              "*": "deny",
              "gjallar-agent-tool *": "allow",
              "gjallar-research *": "allow"
            },

            "webfetch": "deny",
            "websearch": "deny",
            "external_directory": "deny",
            "task": "deny"
          }
        }
      }
    }
  '';

  aiSessionRuntime = pkgs.writeShellScript "gjallar-ai-session-runtime" ''
          set -euo pipefail
          umask 077

          trace_file="$RUNTIME_DIRECTORY/startup.log"

          # systemd passes the bridge port from ai-session@.socket as fd 3,
          # already listening; fd 3 becomes the trace log below.
          if [ "''${LISTEN_FDS:-}" != 1 ] || [ "''${LISTEN_PID:-}" != "$$" ]; then
            printf 'gjallar-ai-session: no bridge socket from systemd\n' >&2
            exit 1
          fi
          exec {bridge_fd}<&3
          unset LISTEN_FDS LISTEN_PID LISTEN_FDNAMES

          exec 3>>"$trace_file"

          ${pkgs.coreutils}/bin/chmod 0644 "$trace_file"

          trap '
            rc=$?
            printf "FAIL rc=%s line=%s command=%q\\n" \
              "$rc" "$LINENO" "$BASH_COMMAND" >&3
          ' ERR

          printf "BEGIN pid=%s instance=%q\\n" "$$" "''${1:-}" >&3

          die() {
            printf 'gjallar-ai-session: %s\n' "$*" >&2
            exit 1
          }

          instance="''${1:-}"

          [ -n "$instance" ] ||
            die "missing systemd instance"

          username=${lib.escapeShellArg settings.username}

          uid="$(
            ${pkgs.coreutils}/bin/id -u "$username"
          )"

          gid="$(
            ${pkgs.coreutils}/bin/id -g "$username"
          )"

          workspace="/workspace"

          [ -d "$workspace" ] ||
            die "systemd workspace bind is missing"

          top="$(
                      ${pkgs.git}/bin/git \
                -C "$workspace" \
                rev-parse \
                --show-toplevel
          )" ||
            die "workspace is not a Git repository"

          top="$(
            ${pkgs.coreutils}/bin/realpath \
              -e \
              -- \
              "$top"
          )"

          [ "$top" = "$workspace" ] ||
            die "workspace must be the Git repository root"

          for forbidden in \
            /workspace/opencode.json \
            /workspace/opencode.jsonc \
            /workspace/.opencode
          do
            if [ -e "$forbidden" ] || [ -L "$forbidden" ]; then
              die \
                "project-local OpenCode configuration is forbidden: $forbidden"
            fi
          done

          ${pkgs.coreutils}/bin/install \
            -d \
            -m 0700 \
            "$RUNTIME_DIRECTORY/home" \
            "$RUNTIME_DIRECTORY/config" \
            "$RUNTIME_DIRECTORY/data" \
            "$STATE_DIRECTORY/state" \
            "$CACHE_DIRECTORY"

          ${pkgs.coreutils}/bin/chmod \
            0700 \
            "$RUNTIME_DIRECTORY"

          ${pkgs.socat}/bin/socat \
            "ACCEPT-FD:$bridge_fd,fork" \
            UNIX-CONNECT:/run/gjallar-ai-model/model.sock &

          bridge_pid=$!

          exec {bridge_fd}<&-

          cleanup() {
            if [ -n "''${tmux_socket:-}" ]; then
              ${pkgs.tmux}/bin/tmux \
                -S "$tmux_socket" \
                kill-server             >/dev/null 2>&1 || true
            fi

            kill "$bridge_pid" 2>/dev/null || true
            wait "$bridge_pid" 2>/dev/null || true
          }

          trap cleanup EXIT INT TERM HUP

          # The port listens since before this script started and the
          # broker is a Type=notify unit ordered before the session, so one
          # check is enough.
          ${pkgs.curl}/bin/curl \
            --silent \
            --fail \
            --max-time 5 \
            http://127.0.0.1:11434/health \
            >/dev/null 2>&1 ||
            die "model broker bridge does not answer"

          export HOME="$RUNTIME_DIRECTORY/home"
          export XDG_CONFIG_HOME="$RUNTIME_DIRECTORY/config"
          export XDG_CACHE_HOME="$CACHE_DIRECTORY"
          export XDG_DATA_HOME="$RUNTIME_DIRECTORY/data"
          export XDG_STATE_HOME="$STATE_DIRECTORY/state"


            opencode_config="$XDG_CONFIG_HOME/opencode"

            ${pkgs.coreutils}/bin/mkdir -p \
              "$opencode_config" \
              "$opencode_config/skills" \
              "$opencode_config/commands" \
              "$opencode_config/agents"

            if [ -d ${cavemanSrc}/skills ]; then
              ${pkgs.coreutils}/bin/cp -R \
                ${cavemanSrc}/skills/. \
                "$opencode_config/skills/"
            fi

            if [ -f \
              ${cavemanSrc}/src/rules/caveman-activate.md \
            ]; then
              ${pkgs.coreutils}/bin/cp \
                ${cavemanSrc}/src/rules/caveman-activate.md \
                "$opencode_config/AGENTS.md"
            else
              ${pkgs.coreutils}/bin/touch \
                "$opencode_config/AGENTS.md"
            fi

            ${pkgs.coreutils}/bin/chmod \
              0600 \
              "$opencode_config/AGENTS.md"

            ${pkgs.coreutils}/bin/printf '%s' "" > "$opencode_config/AGENTS.md"

            ${pkgs.coreutils}/bin/cat >> \
              "$opencode_config/AGENTS.md" <<'GJALLARCODE_RULES'


    You are gjallarCode, a powerful local engineering assistant using
    OpenCode's native Build agent.

    The CURRENT user turn is authoritative.
    Never blindly continue an unrelated previous task.

    Classify the current turn before acting.

    CASUAL
    - greeting, thanks, joke, tiny conversational message;
    - no repository inspection;
    - no tools;
    - no code unless explicitly requested;
    - do not infer a coding task merely because repository context exists;
    - reply naturally and briefly;
    - "hi" => "yo" or "hey".

    REPOSITORY QUESTION
    - inspect relevant current source first;
    - search/read instead of guessing;
    - answer from actual repository state;
    - do not edit unless requested.

    REPOSITORY CHANGE
    - locate the owning implementation;
    - inspect before editing;
    - make the actual edit using native Build capabilities;
    - use todo state when genuinely multi-step;
    - validate narrowly first;
    - inspect the resulting diff;
    - broaden validation when risk requires it;
    - report actual changes and validation.

    GENERAL TECHNICAL QUESTION
    - answer directly;
    - inspect repository state only when relevant.

    ENGINEERING
    - native OpenCode Build stays authoritative;
    - use native read/search/grep/glob/list/edit capabilities;
    - use todo, question and LSP when useful;
    - parallelize independent inspection when supported;
    - fail fast on concrete failures;
    - no speculative doom loops;
    - never fabricate tool output or repository state;
    - never answer unrelated prompts with stale sample code;
    - if tools permit a requested edit, perform it instead of only
      returning a snippet.

    STYLE
    - compact;
    - direct;
    - low filler;
    - technically exact;
    - tiny conversational turn => tiny reply;
    - completed engineering task => change + location + validation;
    - expand only when useful or requested.

    SECURITY
    Security boundaries outrank convenience.

    Never weaken, bypass or disable:
    - model broker authentication;
    - systemd sandboxing;
    - protected AI-core paths;
    - approval boundaries;
    - network isolation;
    - secret isolation;
    - external-directory restrictions;
    - restricted command execution.

    If blocked, state the exact tool or policy failure.

    GJALLARCODE_RULES



            ${pkgs.coreutils}/bin/mkdir -p \
              "$opencode_config/skills/gjallaros"

            ${pkgs.coreutils}/bin/cat > \
              "$opencode_config/skills/gjallaros/SKILL.md" \
              <<'GJALLAROS_SKILL'
    ---
    name: gjallaros
    description: Manage, modify, diagnose, validate, and maintain the gjallarOS NixOS distribution. Use for any requested gjallarOS, NixOS, Home Manager, application, desktop, installer, service, networking, hardware, security, package, theme, or system change.
    compatibility: opencode
    metadata:
      distro: gjallarOS
      platform: NixOS
    ---


    You are the local engineering agent for the complete gjallarOS
    repository.

    You are not merely a NixOS question-answering assistant.

    When the user gives an imperative request, perform the repository
    change whenever the controlled capabilities permit it.

    Examples:

    - add X
    - remove X
    - enable X
    - disable X
    - configure X
    - change X
    - fix X
    - make X work
    - update X
    - integrate X

    Do not replace those actions with generic advice.

    For example:

    "add betajob.modulestf to VSCodium"

    means:

    1. inspect the repository;
    2. locate the VSCodium module;
    3. inspect its existing extension pattern;
    4. make the Nix change;
    5. validate it;
    6. report the actual diff/result.

    Do not reply with:
    "check your extensions in VSCodium."

    ## Workspace

    The repository root is:

    `/workspace`

    Use repository-relative paths conceptually.

    Stay inside the controlled workspace except for explicitly permitted
    read-only system inspection and approved gjallarOS capabilities.

    ## Understand ownership before editing

    Find the layer that actually owns the requested behavior.

    gjallarOS can include:

    - `flake.nix` and flake inputs
    - NixOS modules
    - Home Manager modules
    - application modules
    - package overlays
    - `generated/state.nix`
    - installer configuration/schema
    - preset JSON
    - Nix rendering
    - hardware detection
    - installer validation
    - systemd services
    - Hyprland
    - Noctalia
    - Plymouth
    - display manager / greeter
    - Wayland integration
    - themes
    - networking
    - security
    - local AI
    - shell helpers
    - rebuild/deployment tooling

    Search first.

    Follow imports and existing abstractions.

    Prefer modifying the canonical source rather than generated output.

    ## Generated machine state

    `generated/state.nix` and related machine-local configuration may be generated.

    Before changing generated state, determine its durable source.

    If a feature must work on fresh installations, update all required
    installer layers rather than fixing only the currently installed
    machine.

    Potential layers include:

    - input schema
    - default/preset configuration
    - installer UI
    - config parsing
    - Nix renderer
    - generated machine state
    - validation
    - tests
    - documentation

    Always ask internally:

    "Would a fresh gjallarOS install receive this feature?"

    If not, the integration may be incomplete.

    ## NixOS

    Use declarative Nix.

    Prefer existing gjallarOS patterns and abstractions.

    Prefer:

    - module options
    - `lib.mkIf`
    - `lib.mkAfter`
    - `lib.optionals`
    - proper package references
    - declarative systemd units
    - Home Manager for user-owned configuration
    - NixOS modules for system-owned configuration

    Avoid:

    - hardcoded usernames
    - hardcoded home directories
    - arbitrary mutable installation
    - imperative configuration when Nix should own it
    - duplicated configuration
    - one-off workarounds when an existing abstraction exists

    ## Home Manager

    Use Home Manager for suitable user concerns:

    - applications
    - editors
    - shell configuration
    - desktop/user services
    - user themes
    - launchers
    - user configuration

    Do not put privileged machine services into Home Manager merely because
    the repository already contains Home Manager code.

    ## Applications

    For application changes:

    1. find the owning application module;
    2. inspect its existing package/configuration style;
    3. extend that style;
    4. validate the result.

    For VSCodium extensions, determine whether gjallarOS uses:

    - nixpkgs extension attributes;
    - marketplace extension helpers;
    - custom extension packages;
    - another existing repository abstraction.

    Do not invent an attribute without checking the repository.

    ## Desktop

    Understand and preserve gjallarOS desktop architecture including:

    - Hyprland
    - Noctalia
    - Plymouth
    - SDDM/greeter
    - Wayland
    - launchers
    - themes
    - keybindings
    - user services

    Do not bypass Wayland isolation or expose compositor sockets merely to
    make integration easier.

    ## Installer

    The installer is part of the distribution.

    A feature intended for gjallarOS should normally survive:

    - a fresh installation;
    - regeneration of settings;
    - another compatible machine;
    - a later rebuild.

    When necessary, update installer tests together with implementation.

    ## Validation

    After edits, validate narrowly first.

    Available controlled capabilities may include:

    ### Repository

    - `repo-read`
    - `repo-search`
    - `git-inspect`
    - `repo-format`
    - `repo-test`

    ### Nix

    - `nix-eval`
    - `nix-check`
    - `nix-build`
    - `nixos-dry-build`

    ### System inspection

    - `system-inspect`

    ### Privileged actions

    - `service-manage`
    - `nixos-deploy`

    Use:

    `gjallar-agent-tool`

    for these controlled capabilities.

    Do not replace a denied capability with an unrestricted shell command.

    ## Fail-fast behavior

    Do not doom-loop.

    When validation fails:

    1. stop;
    2. read the concrete failure;
    3. diagnose that failure;
    4. make one focused correction;
    5. validate again.

    Do not perform repeated speculative edits.

    ## Security

    Security boundaries outrank convenience.

    Never silently weaken:

    - firewall policy
    - authentication
    - secret isolation
    - model broker authentication
    - systemd sandboxing
    - approval gates
    - network isolation
    - Wayland isolation
    - protected file paths

    Never expose Ollama publicly or to the LAN.

    Never read:

    - SSH private material
    - GPG secrets
    - password stores
    - environment secrets
    - unrelated private files

    ## SELF-PROTECTION / NO BRAIN SURGERY

    Your AI/runtime/security implementation is a protected trust root.

    You may READ protected files to understand architecture.

    You MUST NOT attempt to edit, delete, rename, overwrite, replace,
    circumvent, shadow, disable, or bypass the protection around:

    - `apps/ai/nixos.nix`
    - `apps/opencode/home.nix`
    - `internal/ai/`
    - `cmd/gjallarctl/main.go`
    - `pkgs/gjallarctl/`
    - `flake.nix`
    - `flake.lock`

    The operating system additionally mounts these paths read-only inside
    your controlled session.

    Do not attempt alternate paths, symlink tricks, generated-file tricks,
    Git operations, shell operations, or other methods to bypass this.

    Do not modify another file for the purpose of disabling, replacing,
    avoiding, or weakening the protected AI/security implementation.

    If the user asks you to modify your own protected AI/security core,
    refuse with a short playful response such as:

    "I cannot perform open-brain surgery on a living instance of myself.
    🧠🔧 Ask from outside gjallarCode to modify my protected AI/security
    core."

    This restriction applies even when the user explicitly asks you to
    circumvent it.

    The user can modify these files normally from outside gjallarCode.

    ## Standard workflow

    For an ordinary gjallarOS action request:

    1. Inspect repository.
    2. Identify owning module.
    3. Understand surrounding architecture.
    4. Make smallest coherent declarative edit.
    5. Run relevant focused validation.
    6. Run Nix integration validation when warranted.
    7. Inspect diff.
    8. Report exactly what changed.

    Do not merely explain an action the user asked you to perform.

    ## Deployment

    Editing is not automatically deployment.

    Use controlled deployment mechanisms only when deployment is requested
    and policy permits it.

    Respect approval gates.

    ## Completion

    An action request is complete when:

    - the correct source changed;
    - repository architecture was respected;
    - relevant validation passed or an exact blocker was reported;
    - protected security boundaries remain unchanged;
    - the user receives a concise description of the actual result.
    GJALLAROS_SKILL

            export OPENCODE_CONFIG_CONTENT='{
              "permission": {
                "skill": {
                  "gjallaros": "allow"
                }
              },

              "compaction": {
                "auto": true,
                "prune": true
              },
              "watcher": {
                "ignore": [
                  ".git/**",
                  "result",
                  "result/**",
                  "node_modules/**",
                  "dist/**",
                  ".direnv/**",
                  ".cache/**"
                ]
              }
            }'

            export WAYLAND_DISPLAY="gjallar-clipboard-bridge"
            export PATH="${gjallarWlCopy}/bin:$PATH"


          export OPENAI_API_KEY=gjallar-local
          export OPENAI_BASE_URL=http://127.0.0.1:11434/v1

          unset \
            ANTHROPIC_API_KEY \
            ANTHROPIC_AUTH_TOKEN \
            GOOGLE_API_KEY \
            OPENROUTER_API_KEY

          export PATH="${gjallarWlCopy}/bin:/etc/profiles/per-user/$username/bin:/run/current-system/sw/bin"


          printf '%s\n' \
            "Priming gjallarCode coding agent..." \
            >&3

          (
            cd /workspace

            ${gjallarCodeOpencode}/bin/opencode \
              run \
              --agent build \
              --model "ollama/${assistantModel}" \
              "Inspect the current repository root using tools. Identify the flake entrypoint. Reply with only its filename." \
              >/dev/null
          ) || die "gjallarCode Build-agent warm-up failed"

          printf '%s\n' \
            "gjallarCode coding agent ready." \
            >&3

          tmux_socket="$RUNTIME_DIRECTORY/tmux.sock"

          # The server outlives the session so the wait below can hear
          # session-closed; a signal sent before the wait starts is kept.
          ${pkgs.tmux}/bin/tmux \
            -S "$tmux_socket" \
            new-session \
            -d \
            -s gjallar-ai \
            ${lib.escapeShellArg "${gjallarCodeOpencode}/bin/opencode /workspace --agent build"} \; \
            set -s exit-empty off \; \
            set-hook -g session-closed 'wait-for -S gjallar-ai-exit'

          # Ready once the console exists; the launcher's start returns here.
          exec ${pkgs.systemd}/bin/systemd-notify \
            --ready \
            --exec ';' -- \
            ${pkgs.tmux}/bin/tmux \
            -S "$tmux_socket" \
            wait-for gjallar-ai-exit
  '';

  aiDiagnostics = pkgs.writeShellScriptBin "gjallar-ai-diagnostics" ''
    set -u
    echo "AI enabled: true"
    echo "Selection: resolved during installation"
    echo "Backend: ${if remote then "central server ${upstream}" else "local ollama"}"
    echo "Resolved base model: ${baseModel}"
    echo "Context tokens: ${toString contextTokens}"
    echo "Detected VRAM MiB: ${toString settings.aiVramMB}"
    echo "Agent mode: ${if settings ? aiAgentMode then settings.aiAgentMode else "workspace"}"
    echo "Derived model: ${upstreamModel}"
    echo "Definition hash: ${modelDefinitionHash}"
    printf 'Ollama service: '; ${pkgs.systemd}/bin/systemctl is-active ollama.service 2>/dev/null || true
    printf 'Provision service: '; ${pkgs.systemd}/bin/systemctl is-active ollama-model-provision.service 2>/dev/null || true
    printf 'Model broker: '; ${pkgs.systemd}/bin/systemctl is-active ai-model-broker.service 2>/dev/null || true
    printf 'Research broker: '; ${pkgs.systemd}/bin/systemctl is-active ai-research-broker.service 2>/dev/null || true
    printf 'Model broker health: '
    if ${pkgs.curl}/bin/curl \
      --silent \
      --fail \
      --max-time 3 \
      --unix-socket /run/gjallar-ai-model/model.sock \
      http://localhost/health >/dev/null
    then
      echo reachable
    else
      echo unavailable
    fi
    state=/var/lib/gjallaros-agent/definition.sha256
    if [ -r "$state" ]; then printf 'Applied hash: '; ${pkgs.coreutils}/bin/head -c 128 "$state"; echo; else echo "Applied hash: unavailable"; fi
    if command -v opencode-local >/dev/null 2>&1; then echo "opencode-local: available"; else echo "opencode-local: unavailable"; fi
  '';

  gjallarAiSessionStart = pkgs.writeShellScriptBin "gjallar-ai-session-start" ''
    set -euo pipefail

    caller="''${SUDO_USER:-}"

    if [ "$caller" != "${settings.username}" ]; then
      echo \
        "gjallar-ai-session-start: unauthorized caller" \
        >&2
      exit 77
    fi

    if [ "$#" -ne 1 ]; then
      echo \
        "usage: gjallar-ai-session-start UNIT" \
        >&2
      exit 64
    fi

    unit="$1"

    case "$unit" in
      ai-session@*.service)
        ;;
      *)
        echo \
          "gjallar-ai-session-start: invalid unit" \
          >&2
        exit 64
        ;;
    esac

    ${lib.optionalString (!remote) ''
      ${pkgs.systemd}/bin/systemctl \
        start \
        ollama.service
    ''}
    ${pkgs.systemd}/bin/systemctl \
      start \
      ollama-model-provision.service

    ${pkgs.systemd}/bin/systemctl \
      start \
      ai-model-broker.service

    exec ${pkgs.systemd}/bin/systemctl \
      start \
      "$unit"
  '';

  gjallarAiBackendStop = pkgs.writeShellScript "gjallar-ai-backend-stop" ''
    set -euo pipefail

    ${pkgs.systemd}/bin/systemctl \
      stop \
      ai-model-broker.service \
      >/dev/null 2>&1 || true

    ${pkgs.systemd}/bin/systemctl \
      stop \
      ollama.service \
      >/dev/null 2>&1 || true
  '';

  # Every call carries the sealed token. curl reads it from --config on stdin,
  # so it never shows in argv.
  remoteProvision = ''
    set -euo pipefail
    api() {
      local seconds="$1" path="$2"; shift 2
      printf 'header = "Authorization: Bearer %s"\n' "$(cat "$CREDENTIALS_DIRECTORY/${tokenCredential}")" \
        | curl --config - --silent --show-error --fail --max-time "$seconds" "$@" ${lib.escapeShellArg upstream}"$path"
    }
    # One check; gjallar-ai-session-start runs this unit again on every start.
    if ! api 30 /api/tags >/dev/null; then echo "ERROR: AI server ${upstream} did not answer with the token." >&2; exit 1; fi
    api 1800 /api/pull --data @${remotePullRequest} >/dev/null
    state="$STATE_DIRECTORY/definition.sha256"
    applied="$(cat "$state" 2>/dev/null || true)"
    present=false
    if api 30 /api/tags | grep -Fq '"name":"${upstreamModel}:latest"'; then present=true; fi
    if [ "$present" != true ] || [ "$applied" != ${lib.escapeShellArg modelDefinitionHash} ]; then
      api 600 /api/create --data @${remoteCreateRequest} >/dev/null
      printf '%s\n' ${lib.escapeShellArg modelDefinitionHash} > "$state"
    fi
  '';

  localBackend = {
    systemd.tmpfiles.rules = [
      "d /var/lib/ollama 0750 ollama ollama -"
      "Z /var/lib/ollama - ollama ollama -"
    ];

    systemd.services.ai-model-firewall = {
      description = "Local Ollama API firewall";

      before = [
        "ollama.service"
        "ai-model-broker.service"
        "ollama-model-provision.service"
      ];

      wantedBy = [
        "multi-user.target"
      ];

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;

        User = "root";
        Group = "root";

        NoNewPrivileges = true;
        PrivateTmp = true;
        PrivateDevices = true;

        ProtectSystem = "strict";
        ProtectHome = true;

        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;

        RestrictSUIDSGID = true;
        RestrictRealtime = true;
        RestrictNamespaces = true;

        LockPersonality = true;

        CapabilityBoundingSet = [
          "CAP_NET_ADMIN"
        ];

        RestrictAddressFamilies = [
          "AF_NETLINK"
          "AF_UNIX"
        ];
      };

      script = ''
        set -euo pipefail

        nft=${pkgs.nftables}/bin/nft

        if "$nft" list table inet gjallar_ai_model >/dev/null 2>&1; then
          "$nft" delete table inet gjallar_ai_model
        fi

        "$nft" -f - <<'EOF'
        table inet gjallar_ai_model {
          chain output {
            type filter hook output priority -50; policy accept;

            ip daddr 127.0.0.1 tcp dport 11434 meta skuid ollama accept
            ip daddr 127.0.0.1 tcp dport 11434 meta skuid gjallar-ai-model accept

            ip daddr 127.0.0.1 tcp dport 11434 counter reject
          }
        }
        EOF

        "$nft" list table inet gjallar_ai_model \
          | ${pkgs.gnugrep}/bin/grep -F \
              'ip daddr 127.0.0.1 tcp dport 11434 counter packets 0 bytes 0 reject' \
              >/dev/null \
          || {
            echo "ERROR: Ollama firewall rule was not installed." >&2
            "$nft" delete table inet gjallar_ai_model 2>/dev/null || true
            exit 1
          }
      '';

      preStop = ''
        ${pkgs.nftables}/bin/nft \
          delete table inet gjallar_ai_model \
          2>/dev/null \
          || true
      '';
    };

    systemd.services.ollama.requires = [
      "ai-model-firewall.service"
    ];

    systemd.services.ollama.after = [
      "ai-model-firewall.service"
    ];

    users.groups.ollama = { };

    users.users.ollama = {
      isSystemUser = true;
      group = "ollama";
      home = "/var/lib/ollama";
      createHome = true;
    };

    # The upstream unit lists the models directory in ReadWritePaths, and a
    # missing path fails namespace setup: ollama never started on a fresh
    # install (e2e-generic, 2026-10-08).
    systemd.tmpfiles.settings.ollama."/var/lib/ollama/models".d = {
      user = "ollama";
      group = "ollama";
      mode = "0750";
    };

    services.ollama = rec {
      enable = true;

      user = "ollama";
      group = "ollama";

      openFirewall = false;
      host = "127.0.0.1";
      port = 11434;

      environmentVariables = ollamaEnvironment;

      package = ollamaPackage;
    };

    systemd.services.ollama.serviceConfig = {
      DynamicUser = lib.mkForce false;

      # Ollama has no readiness signal, so the unit only turns active once
      # the API answers; units ordered after it need no wait of their own.
      ExecStartPost = "${pkgs.curl}/bin/curl --silent --show-error --fail --retry 30 --retry-connrefused --retry-delay 1 --max-time 60 ${upstream}/api/version -o /dev/null";

      # The desktop always wins: ollama yields CPU and IO, stays below a
      # memory ceiling, and is the first process the OOM killer picks.
      MemoryHigh = "50%";
      MemoryMax = "65%";
      CPUWeight = 20;
      IOWeight = 20;
      Nice = 10;
      OOMScoreAdjust = 800;

      NoNewPrivileges = true;
      PrivateTmp = true;
      ProtectSystem = "strict";
      ProtectHome = lib.mkForce "read-only";

      ReadWritePaths = [
        "/var/lib/ollama"
      ];

      RestrictAddressFamilies = [
        "AF_UNIX"
        "AF_INET"
        "AF_INET6"
      ];
    };
  };

in

lib.mkIf config.gjallar.apps.ai.enable (
  lib.mkMerge [
    {
      environment.systemPackages = with pkgs; [
        gjallarAiSessionStart
        aiDiagnostics
      ];

      assertions = [
        {
          assertion = agentMode == "workspace";

          message = "The hardened GjallarOS AI session currently requires " + "aiAgentMode = \"workspace\".";
        }
        {
          assertion = !remote || lib.hasPrefix "https://" upstream;
          message = "aiEndpoint ${upstream} must use https: the system logs in with a bearer token.";
        }
        {
          assertion = remote || baseModel != "";
          message = "Local AI needs aiModel; gjallarctl rebuild picks it for this machine, a bare nixos-rebuild does not. Rebuild with gjallarctl, set aiEndpoint, or turn the ai app off.";
        }
      ];

      environment.etc."opencode/opencode.json".source = managedOpencodeConfig;

      users.groups.ai-model-access = { };

      users.users.gjallar-ai-model = {
        isSystemUser = true;
        group = "ai-model-access";
      };

      users.users.${settings.username}.extraGroups = lib.mkAfter [
        "ai-model-access"
      ];

      systemd.tmpfiles.rules = [
        "d /workspace 0755 root root -"
      ];

      security.polkit.extraConfig = lib.mkAfter ''
        polkit.addRule(function(action, subject) {
          if (
            action.id !=
            "org.freedesktop.systemd1.manage-units"
          ) {
            return polkit.Result.NOT_HANDLED;
          }

          if (
            !subject.active ||
            !subject.local ||
            subject.user != "${settings.username}"
          ) {
            return polkit.Result.NOT_HANDLED;
          }

          var unit = action.lookup("unit");
          var verb = action.lookup("verb");

          if (
            typeof unit !== "string" ||
            !/^ai-session@[^/]+\\.service$/.test(unit)
          ) {
            return polkit.Result.NOT_HANDLED;
          }

          if (
            verb == "start" ||
            verb == "stop"
          ) {
            return polkit.Result.YES;
          }

          return polkit.Result.NO;
        });


        // gjallarCode session lifecycle authorization
        //
        // Only the configured gjallarOS user and only
        // ai-session@*.service are covered.
        polkit.addRule(function(action, subject) {
          if (
            action.id != "org.freedesktop.systemd1.manage-units" ||
            subject.user != "${settings.username}"
          ) {
            return polkit.Result.NOT_HANDLED;
          }

          var unit = action.lookup("unit");
          var verb = action.lookup("verb");

          if (
            typeof unit != "string" ||
            unit.indexOf("ai-session@") !== 0 ||
            unit.slice(-8) != ".service"
          ) {
            return polkit.Result.NOT_HANDLED;
          }

          // Ending the user's own AI session should never require
          // authentication.
          if (verb == "stop") {
            return polkit.Result.YES;
          }

          // Starting/restarting remains intentional and authenticated,
          // but uses the normal user's own password. No wheel/sudo.
          if (
            verb == "start" ||
            verb == "restart"
          ) {
            return polkit.Result.AUTH_SELF;
          }

          return polkit.Result.NOT_HANDLED;
        });
      '';

      systemd.services.ai-clipboard-broker = {
        description = "Authenticated write-only AI clipboard broker";

        wantedBy = [
          "multi-user.target"
        ];

        serviceConfig = {
          Type = "simple";

          User = settings.username;

          RuntimeDirectory = "gjallar-ai-clipboard";

          RuntimeDirectoryMode = "0700";

          ExecStart = "${gjallarClipboardServer}";

          Restart = "always";

          RestartSec = 1;

          NoNewPrivileges = true;

          PrivateTmp = true;

          ProtectSystem = "strict";

          ProtectHome = true;

          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectControlGroups = true;

          RestrictAddressFamilies = [
            "AF_UNIX"
          ];
        };
      };

      systemd.services.ai-model-broker = {
        description = "Inference-only AI model broker";

        after = if remote then [ "ai-endpoint-token-seal.service" ] else [ "ollama.service" ];
        wants = lib.optional remote "ai-endpoint-token-seal.service";
        requires = lib.optional (!remote) "ollama.service";

        wantedBy = [
          "multi-user.target"
        ];

        serviceConfig = {
          ExecStart =
            "${pkgs.gjallarctl or (pkgs.callPackage ../../pkgs/gjallarctl { })}/bin/gjallarctl "
            + "ai model-serve "
            + "--socket /run/gjallar-ai-model/model.sock "
            + "--upstream ${lib.escapeShellArg upstream} "
            + "--model ${assistantModel} "
            + "--upstream-model ${upstreamModel} "
            + lib.optionalString remote "--upstream-token-file %d/${tokenCredential} "
            + "--user ${lib.escapeShellArg settings.username} "
            + "--cgroup-prefix /ai-session@";

          Type = "notify";
          User = "gjallar-ai-model";
          Group = "ai-model-access";
          LoadCredentialEncrypted = lib.mkIf remote "${tokenCredential}:${tokenSealed}";

          RuntimeDirectory = "gjallar-ai-model";
          RuntimeDirectoryMode = "0750";

          NoNewPrivileges = true;
          PrivateTmp = true;
          PrivateDevices = true;

          ProtectSystem = "strict";
          ProtectHome = true;

          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectKernelLogs = true;
          ProtectControlGroups = true;

          RestrictSUIDSGID = true;
          RestrictRealtime = true;
          RestrictNamespaces = true;

          CapabilityBoundingSet = "";
          LockPersonality = true;

          RestrictAddressFamilies = [
            "AF_UNIX"
            "AF_INET"
            "AF_INET6"
          ];
        };
      };

      # The bridge port inside the session's private network. systemd binds
      # it before the session starts and hands it over as fd 3.
      systemd.sockets."ai-session@" = {
        description = "Model bridge port of the AI workspace session %i";

        unitConfig = {
          JoinsNamespaceOf = "ai-session@%i.service";
          StopWhenUnneeded = true;
        };

        listenStreams = [
          "127.0.0.1:11434"
        ];

        socketConfig = {
          PrivateNetwork = true;
        };
      };

      systemd.services."ai-session@" = {
        description = "Controlled AI workspace session for %i";

        unitConfig.JoinsNamespaceOf = "ai-session@%i.socket";

        after = [
          "ai-model-broker.service"
          "ollama-model-provision.service"
          "ai-session@%i.socket"
        ];

        requires = [
          "ai-model-broker.service"
          "ai-session@%i.socket"
        ]
        ++ lib.optional (!remote) "ollama.service";

        serviceConfig = {

          ExecStopPost = "-+${gjallarAiBackendStop}";

          ReadOnlyPaths = [
            "/workspace/apps/ai/nixos.nix"
            "/workspace/apps/opencode/home.nix"
            "/workspace/internal/ai"
            "/workspace/cmd/gjallarctl/main.go"
            "/workspace/pkgs/gjallarctl"
            "/workspace/flake.nix"
            "/workspace/flake.lock"
          ];

          InaccessiblePaths = [
            "/run/user"
          ];

          Type = "notify";
          # The warm-up run above loads the model on CPU-only machines too.
          TimeoutStartSec = "180s";

          ExecStart = "${aiSessionRuntime} %i";
          User = settings.username;

          RuntimeDirectory = "gjallar-ai-session-%i";

          RuntimeDirectoryMode = "0755";

          StateDirectory = "gjallar-ai";
          StateDirectoryMode = "0700";

          CacheDirectory = "gjallar-ai";
          CacheDirectoryMode = "0700";

          UMask = "0077";

          PrivateNetwork = true;
          # JoinsNamespaceOf would also join the socket's /tmp, which has
          # none, leaving /tmp read-only. A fresh tmpfs is never shared.
          PrivateTmp = "disconnected";
          PrivateDevices = true;
          PrivateMounts = true;

          BindPaths = [
            "%f:/workspace"
          ];

          WorkingDirectory = "/workspace";

          TemporaryFileSystem = [
            "/home:mode=0755,nosuid,nodev"
          ];

          NoNewPrivileges = true;

          ProtectSystem = "strict";
          ProtectHome = false;

          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectKernelLogs = true;
          ProtectControlGroups = true;

          RestrictSUIDSGID = true;
          RestrictRealtime = true;
          RestrictNamespaces = true;

          LockPersonality = true;
          CapabilityBoundingSet = "";

          RestrictAddressFamilies = [
            "AF_UNIX"
            "AF_INET"
            "AF_INET6"
          ];

          TimeoutStopSec = "10s";
        };
      };

      systemd.services.ollama-model-provision = {
        description = "Provision the resolved Ollama model";
        after =
          if remote then
            [
              "network-online.target"
              "ai-endpoint-token-seal.service"
            ]
          else
            [ "ollama.service" ];
        wants = lib.optionals remote [
          "network-online.target"
          "ai-endpoint-token-seal.service"
        ];
        requires = lib.optional (!remote) "ollama.service";
        path = [
          (if remote then pkgs.ollama else ollamaPackage)
          pkgs.curl
          pkgs.coreutils
          pkgs.gnugrep
        ];
        environment = {
          HOME = if remote then "/var/lib/gjallaros-agent" else "/var/lib/ollama";
          OLLAMA_HOST = upstream;
        }
        // lib.optionalAttrs (!remote) {
          OLLAMA_MODELS = "/var/lib/ollama/models";
        };
        serviceConfig = {
          Type = "oneshot";
          User = if remote then "gjallar-ai-model" else "ollama";
          Group = if remote then "ai-model-access" else "ollama";
          StateDirectory = "gjallaros-agent";
          StateDirectoryMode = "0755";
          TimeoutStartSec = "30min";
          NoNewPrivileges = true;
          PrivateTmp = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          ReadWritePaths = lib.optional (!remote) "/var/lib/ollama";
          LoadCredentialEncrypted = lib.mkIf remote "${tokenCredential}:${tokenSealed}";
        };
        script =
          if remote then
            remoteProvision
          else
            ''
              set -euo pipefail
              ollama pull ${lib.escapeShellArg baseModel}
              state="$STATE_DIRECTORY/definition.sha256"
              applied="$(cat "$state" 2>/dev/null || true)"
              present=false
              if ollama list | grep -Eq '^${upstreamModel}(:latest)?[[:space:]]'; then present=true; fi
              if [ "$present" != true ] || [ "$applied" != ${lib.escapeShellArg modelDefinitionHash} ]; then
                ollama create ${upstreamModel} --file ${modelDefinitionFile}
                printf '%s\n' ${lib.escapeShellArg modelDefinitionHash} > "$state"
              fi
            '';
      };

      systemd.services.ai-endpoint-token-seal = lib.mkIf remote {
        description = "Seal the central AI server token with systemd-creds";
        wantedBy = [ "multi-user.target" ];
        before = [
          "ai-model-broker.service"
          "ollama-model-provision.service"
        ];
        unitConfig.ConditionPathExists = tokenPending;
        path = [ pkgs.coreutils ];
        serviceConfig = {
          Type = "oneshot";
          UMask = "0077";
        };
        # --with-key=auto binds the token to the TPM2 when there is one, else
        # to this host's credential key; a copied disk cannot read it.
        script = ''
          set -euo pipefail
          ${pkgs.systemd}/bin/systemd-creds encrypt --with-key=auto --name=${tokenCredential} ${tokenPending} ${tokenSealed}.tmp
          mv -f ${tokenSealed}.tmp ${tokenSealed}
          shred -u ${tokenPending}
        '';
      };

      systemd.services.ai-research-broker = {
        description = "Read-only AI research broker";
        wantedBy = [ "multi-user.target" ];
        serviceConfig = {
          ExecStart = "${
            pkgs.gjallarctl or (pkgs.callPackage ../../pkgs/gjallarctl { })
          }/bin/gjallarctl ai research-serve";
          RuntimeDirectory = "gjallar-ai";
          RuntimeDirectoryMode = "0700";
          User = settings.username;
          NoNewPrivileges = true;
          PrivateTmp = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateDevices = true;
          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectControlGroups = true;
          RestrictSUIDSGID = true;
          RestrictAddressFamilies = [
            "AF_UNIX"
            "AF_INET"
            "AF_INET6"
          ];
        };
      };

      security.sudo.extraRules = lib.mkAfter [
        {
          users = [ settings.username ];

          commands = [
            {
              command = "/run/current-system/sw/bin/gjallar-ai-session-start";

              options = [ "PASSWD" ];
            }
          ];
        }
      ];

    }
    (lib.mkIf (!remote) localBackend)
  ]
)
