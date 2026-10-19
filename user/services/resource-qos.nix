{ pkgs, ... }:

let
  managedWeight = 80;
  foregroundWeight = 100;
  explicitBackgroundWeight = 25;

  gjallarRun = pkgs.writeShellApplication {
    name = "gjallar-run";

    runtimeInputs = [
      pkgs.app2unit
    ];

    text = ''
      # Hyprland starts before the interactive shell, so GUI keybinds do not
      # automatically inherit Home Manager's session environment. Load it
      # here so managed GUI launches see the same XDG/Qt/theme policy.
      for hm_session_vars in \
        "/etc/profiles/per-user/$USER/etc/profile.d/hm-session-vars.sh" \
        "$HOME/.nix-profile/etc/profile.d/hm-session-vars.sh"
      do
        if [ -r "$hm_session_vars" ]; then
          # shellcheck disable=SC1090
          source "$hm_session_vars"
          break
        fi
      done

      family="desktop"

      if [ "$#" -ge 2 ] && [ "$1" = "--family" ]; then
        family="$2"
        shift 2
      fi

      if [ "$#" -eq 0 ]; then
        echo "usage: gjallar-run [--family desktop|jods|ai|background] COMMAND [ARG...]" >&2
        exit 64
      fi

      command_name="''${1##*/}"

      case "$family" in
        desktop)
          app_name="gjallar-$command_name"
          slice="a"
          weight=${toString managedWeight}
          ;;
        jods)
          app_name="jods-$command_name"
          slice="a"
          weight=${toString managedWeight}
          ;;
        ai)
          app_name="gjallar-ai-$command_name"
          slice="a"
          weight=${toString managedWeight}
          ;;
        background)
          app_name="background-$command_name"
          slice="b"
          weight=${toString explicitBackgroundWeight}
          ;;
        *)
          echo "gjallar-run: unknown workload family: $family" >&2
          exit 64
          ;;
      esac

      exec app2unit \
        -t scope \
        -s "$slice" \
        -a "$app_name" \
        -p "CPUWeight=$weight" \
        -- "$@"
    '';
  };

  gjallarResourceQos = pkgs.writeShellApplication {
    name = "gjallar-resource-qos";

    runtimeInputs = with pkgs; [
      coreutils
      gnused
      hyprland
      socat
      systemd
    ];

    text = ''
      unfocused_weight=${toString managedWeight}
      foreground_weight=${toString foregroundWeight}
      current_unit=""

      set_weight() {
        unit="$1"
        weight="$2"

        [ -n "$unit" ] || return 0

        systemctl --user set-property \
          --runtime \
          "$unit" \
          "CPUWeight=$weight" \
          >/dev/null 2>&1 || true
      }

      focused_owned_unit() {
        active="$(
          hyprctl activewindow -j 2>/dev/null || true
        )"

        pid="$(
          printf '%s\n' "$active" |
            sed -n \
              's/^[[:space:]]*"pid":[[:space:]]*\([0-9][0-9]*\),\{0,1\}$/\1/p' |
            head -n 1
        )"

        [ -n "$pid" ] || return 1
        [ -r "/proc/$pid/cgroup" ] || return 1

        cgroup="$(
          sed -n 's#^0::##p' "/proc/$pid/cgroup"
        )"

        unit="''${cgroup##*/}"

        case "$unit" in
          app-*-gjallar\\x2dai\\x2d*.scope|app-*-gjallar-ai-*.scope)
            return 1
            ;;
          app-*-gjallar\\x2d*.scope|app-*-gjallar-*.scope)
            printf '%s\n' "$unit"
            ;;
          *)
            return 1
            ;;
        esac
      }

      update_focus() {
        next_unit="$(focused_owned_unit || true)"

        [ "$next_unit" = "$current_unit" ] && return 0

        if [ -n "$current_unit" ]; then
          set_weight "$current_unit" "$unfocused_weight"
        fi

        if [ -n "$next_unit" ]; then
          set_weight "$next_unit" "$foreground_weight"
        fi

        current_unit="$next_unit"
      }

      runtime_dir="''${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"

      if [ -z "''${HYPRLAND_INSTANCE_SIGNATURE:-}" ]; then
        printf '%s\n' \
          'gjallar-resource-qos: HYPRLAND_INSTANCE_SIGNATURE is unavailable' \
          >&2
        exit 1
      fi

      event_socket="$runtime_dir/hypr/$HYPRLAND_INSTANCE_SIGNATURE/.socket2.sock"

      # Hyprland listens on socket2 before it starts hyprland-session.target,
      # so the socket is there when this unit starts.
      update_focus

      while IFS= read -r event; do
        case "$event" in
          activewindowv2'>>'*)
            update_focus
            ;;
        esac
      done < <(
        socat -U - "UNIX-CONNECT:$event_socket"
      )

      # Hyprland removes hyprland.lock when it quits (its sockets stay), so a
      # missing lock means the session is over. Otherwise the stream broke:
      # fail and let systemd restart the unit.
      if [ ! -e "$runtime_dir/hypr/$HYPRLAND_INSTANCE_SIGNATURE/hyprland.lock" ]; then
        exit 0
      fi
      printf '%s\n' 'gjallar-resource-qos: Hyprland event stream ended' >&2
      exit 1
    '';
  };

in
{
  _module.args.gjallarRun = gjallarRun;

  home.packages = [
    gjallarRun
    (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-run" ''
      #compdef gjallar-run
      _arguments \
        '--family[resource family]:family:(desktop jods ai background)' \
        '*::command:_normal'
    '')
  ];

  systemd.user.services.interactive-resource-qos = {
    Unit = {
      Description = "Interactive application QoS";
      After = [ "hyprland-session.target" ];
      PartOf = [ "hyprland-session.target" ];
    };

    Service = {
      Type = "simple";
      ExecStart = "${gjallarResourceQos}/bin/gjallar-resource-qos";
      Restart = "on-failure";
      RestartSec = 2;
    };

    Install.WantedBy = [ "hyprland-session.target" ];
  };
}
