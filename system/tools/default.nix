{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  diagnosticsEnabled = settings.debugFunctions or false;
  systemctl = "${pkgs.systemd}/bin/systemctl";
  systemdEscape = "${pkgs.systemd}/bin/systemd-escape";
  coreutils = "${pkgs.coreutils}/bin";
  bootDiagnostic = pkgs.writeShellScript "gjallar-boot-diagnostics" ''
    set -u
    log_file="/var/lib/gjallar-diagnostics/boot-$(${coreutils}/cat /proc/sys/kernel/random/boot_id).log"
    exec > >(${coreutils}/tee "$log_file") 2>&1

    log() { printf 'gjallar-boot-diagnostics: %s\n' "$*"; }
    report_unit() {
      local unit="$1"
      log "unit=$unit"
      ${systemctl} show "$unit" --property=LoadState --property=ActiveState --property=SubState --property=Result --no-pager || true
      ${systemctl} status "$unit" --no-pager --full || true
    }
    report_home() {
      local home="$1"
      if ${coreutils}/test -L "$home/.config/hypr/hyprland.lua"; then
        log "hyprland-config=$(${coreutils}/readlink -f "$home/.config/hypr/hyprland.lua")"
      else
        log "hyprland-config missing: $home/.config/hypr/hyprland.lua"
      fi
    }

    log 'begin'
    ${coreutils}/date --iso-8601=seconds
    ${coreutils}/uname -a
    ${coreutils}/df -h / /boot /home /nix | ${coreutils}/uniq || true
    for kind in cpu memory io; do
      log "pressure-$kind $(${coreutils}/tr '\n' ' ' </proc/pressure/$kind)"
    done
    ${systemctl} --failed --no-pager || true
    report_unit NetworkManager.service
    report_unit display-manager.service
    report_unit "home-manager-$(${systemdEscape} -- "${settings.username}").service"
    report_home "/home/${settings.username}"
    ${pkgs.networkmanager}/bin/nmcli general status || true
    ${pkgs.networkmanager}/bin/nmcli device status || true
    ${pkgs.systemd}/bin/journalctl -b -p warning..alert --no-pager || true
    # How the boot before ended: a hang, a crash or a dead session leaves
    # its trace there, and nothing else looks back.
    log 'previous-boot errors'
    ${pkgs.systemd}/bin/journalctl -b -1 -p err..alert -n 60 --no-pager || true
    log 'previous-boot coredumps'
    ${pkgs.systemd}/bin/journalctl -b -1 -t systemd-coredump -o cat -n 20 --no-pager || true
    log 'previous-boot last lines'
    ${pkgs.systemd}/bin/journalctl -b -1 -n 40 -o short-iso --no-pager || true
    log 'end'
  '';
  # Snapshots of who uses the CPU, memory and disk while the system stalls.
  # Only process names (comm) are logged, never command lines or environment.
  pressureMonitor = pkgs.writeShellScript "gjallar-pressure-monitor" ''
    set -u
    log_file="/var/lib/gjallar-diagnostics/pressure-$(${coreutils}/cat /proc/sys/kernel/random/boot_id).log"
    max_bytes=$((4 * 1024 * 1024))
    # Each reason has its own limit, so an I/O stall at login does not hide
    # a CPU stall a minute later.
    declare -A last_snapshot=()

    # Integer part of avg10 on the "some" or "full" line.
    avg10() {
      ${pkgs.gawk}/bin/awk -v line="$2" '$1 == line { split($2, a, "="); printf "%d", a[2] }' "/proc/pressure/$1"
    }
    # getline skips a process that exited or may not be read; a plain awk
    # input file stops awk at the first one, so the list came out empty.
    io_bytes() {
      ${pkgs.gawk}/bin/awk '
        BEGIN {
          for (i = 1; i < ARGC; i++) {
            split(ARGV[i], p, "/"); total = 0; found = 0
            while ((getline line < ARGV[i]) > 0)
              if (line ~ /^(read|write)_bytes:/) { split(line, f, " "); total += f[2]; found = 1 }
            close(ARGV[i])
            if (found) print p[3], total
          }
        }
      ' /proc/[0-9]*/io
    }
    snapshot() {
      printf '=== %s %s\n' "$(${coreutils}/date --iso-8601=seconds)" "$1"
      ${coreutils}/cat /proc/loadavg
      for kind in cpu memory io; do
        printf '%s: %s\n' "$kind" "$(${coreutils}/tr '\n' ' ' </proc/pressure/$kind)"
      done
      ${pkgs.procps}/bin/free -m
      ${coreutils}/df -h / /home /nix 2>/dev/null | ${coreutils}/uniq
      echo '--- cpu (top, 1 s)'
      ${pkgs.procps}/bin/top -b -n 2 -d 1 -w 120 -o %CPU |
        ${pkgs.gawk}/bin/awk '/^top -/ { n++ } n == 2' | ${coreutils}/head -n 20
      echo '--- memory (rss)'
      ${pkgs.procps}/bin/ps -eo pid,rss,pmem,comm --sort=-rss | ${coreutils}/head -n 11
      echo '--- disk (read+write over 2 s)'
      before="$(io_bytes)"
      ${coreutils}/sleep 2
      ${pkgs.gawk}/bin/awk '
        NR == FNR { seen[$1] = $2; next }
        ($1 in seen) && $2 > seen[$1] { print $2 - seen[$1], $1 }
      ' <(printf '%s\n' "$before") <(io_bytes) | ${coreutils}/sort -rn | ${coreutils}/head -n 8 |
        while read -r bytes pid; do
          printf '%8.1f MB/s %7s %s\n' "$(${pkgs.gawk}/bin/awk -v b="$bytes" 'BEGIN { print b / 2 / 1048576 }')" \
            "$pid" "$(${coreutils}/cat "/proc/$pid/comm" 2>/dev/null || echo gone)"
        done
    }

    while :; do
      reasons=""
      [ "$(avg10 io full)" -ge 20 ] && reasons="$reasons io-full"
      [ "$(avg10 memory full)" -ge 10 ] && reasons="$reasons memory-full"
      [ "$(avg10 cpu some)" -ge 60 ] && reasons="$reasons cpu-some"
      now="$(${coreutils}/date +%s)"
      due=""
      for reason in $reasons; do
        if [ $((now - ''${last_snapshot[$reason]:-0})) -ge 120 ]; then
          last_snapshot[$reason]="$now"
          due="$due $reason"
        fi
      done
      if [ -n "$due" ]; then
        reasons="$due"
        echo "gjallar-pressure-monitor: stall:$reasons"
        size="$(${coreutils}/stat -c %s "$log_file" 2>/dev/null || echo 0)"
        if [ "$size" -lt "$max_bytes" ]; then
          snapshot "$reasons" >>"$log_file" 2>&1
        else
          echo "gjallar-pressure-monitor: $log_file is full, journal only"
          snapshot "$reasons" 2>&1
        fi
      fi
      ${coreutils}/sleep 10
    done
  '';
  homeDiagnosticsModule =
    { config, ... }:
    let
      sessionDiagnostic = pkgs.writeShellScript "gjallar-hyprland-session-diagnostics" ''
        set -u
        state_dir="$HOME/.local/state/gjallar-diagnostics"
        ${coreutils}/mkdir -p "$state_dir"
        boot_id="$(${coreutils}/cat /proc/sys/kernel/random/boot_id)"
        exec > >(${coreutils}/tee -a "$state_dir/hyprland-$boot_id.log") 2>&1
        log() { printf 'gjallar-hyprland-session-diagnostics: %s\n' "$*"; }
        log 'begin'
        ${coreutils}/date --iso-8601=seconds
        ${coreutils}/id
        ${pkgs.systemd}/bin/systemctl --user show hyprland-session.target --property=ActiveState --property=SubState --property=Result --no-pager || true
        # Login to desktop: from the user's first login on this boot to the
        # session target. A slow start shows up here.
        login_at="$(${pkgs.systemd}/bin/loginctl show-user "$USER" --property=Timestamp --value 2>/dev/null || true)"
        ready_at="$(${pkgs.systemd}/bin/systemctl --user show hyprland-session.target --property=ActiveEnterTimestamp --value 2>/dev/null || true)"
        if [ -n "$login_at" ] && [ -n "$ready_at" ]; then
          log "login-to-session-seconds=$(( $(${coreutils}/date -d "$ready_at" +%s) - $(${coreutils}/date -d "$login_at" +%s) ))"
        fi
        ${coreutils}/printenv | ${pkgs.gnugrep}/bin/grep -E '^(PATH|XDG_|WAYLAND_DISPLAY|HYPRLAND_INSTANCE_SIGNATURE|DBUS_SESSION_BUS_ADDRESS)=' || true
        # Hyprland opens its IPC socket during init and starts
        # hyprland-session.target later from its hyprland.start hook, so one
        # try is enough; false here is a real finding.
        hyprland_ready=false
        if ${pkgs.hyprland}/bin/hyprctl monitors >/dev/null 2>&1; then
          hyprland_ready=true
        fi
        log "hyprland-ipc-ready=$hyprland_ready"
        for command in fuzzel ghostty noctalia; do
          path="$(${pkgs.findutils}/bin/find "$HOME/.nix-profile/bin" "/etc/profiles/per-user/$USER/bin" -maxdepth 1 -name "$command" \( -type l -o -type f \) -print -quit 2>/dev/null || true)"
          if [ -n "$path" ]; then log "command=$command path=$path"; else log "command=$command missing from user profiles"; fi
        done
        if ${coreutils}/test -L "$HOME/.config/hypr/hyprland.lua"; then
          log "hyprland-config=$(${coreutils}/readlink -f "$HOME/.config/hypr/hyprland.lua")"
        else
          log 'hyprland-config missing'
        fi
        ${pkgs.hyprland}/bin/hyprctl version || true
        ${pkgs.hyprland}/bin/hyprctl configerrors || true
        ${pkgs.hyprland}/bin/hyprctl monitors || true
        # Window titles can carry document names and web pages: app and
        # workspace only.
        ${pkgs.hyprland}/bin/hyprctl clients -j | ${pkgs.jq}/bin/jq -r '.[] | "client class=\(.class) workspace=\(.workspace.name) pid=\(.pid)"' || true
        ${lib.getExe config.programs.noctalia.package} config validate || true
        # Noctalia can start after this unit, so false here is no finding alone.
        noctalia_ready=false
        if ${lib.getExe config.programs.noctalia.package} msg status >/dev/null 2>&1; then
          noctalia_ready=true
        fi
        log "noctalia-ipc-ready-at-diagnostics=$noctalia_ready"
        ${pkgs.procps}/bin/pgrep -f -l -u "$USER" 'Hyprland|noctalia|fuzzel|ghostty|swaybg|waybar' || true
        log 'end'
      '';
    in
    {
      systemd.user.services.hyprland-session-diagnostics = {
        Unit = {
          Description = "Hyprland session diagnostics";
          After = [ "hyprland-session.target" ];
          PartOf = [ "hyprland-session.target" ];
        };
        Service = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = sessionDiagnostic;
        };
        Install.WantedBy = [ "hyprland-session.target" ];
      };
      systemd.user.tmpfiles.rules = [ "d %h/.local/state/gjallar-diagnostics 0700 - - 30d" ];
    };
in
{
  imports = [
    ./android.nix
    ./chromium.nix
    ./commands/default.nix
    ./fido2.nix
    ./network-diagnostics.nix
  ];

  config = lib.mkMerge [
    {
      boot = {
        plymouth.enable = true;

        consoleLogLevel = 0;
        initrd.verbose = false;

        kernelParams = [
          "quiet"
          "splash"
          "rd.systemd.show_status=false"
          "systemd.show_status=false"
          "udev.log_level=0"
          "rd.udev.log_level=0"
        ];
      };

      # The stock quit units end Plymouth before greetd and drop the splash,
      # so the console shows through until the greeter draws. greetd quits it
      # itself, keeping the last frame up until the greeter takes the display.
      systemd.services.plymouth-quit.enable = false;
      systemd.services.plymouth-quit-wait.enable = false;

      systemd.services.greetd.serviceConfig.ExecStartPre =
        let
          plymouth = "${config.boot.plymouth.package}/bin/plymouth";
        in
        pkgs.writeShellScript "handoff-plymouth-to-greetd" ''
          # No daemon: the splash is off or already gone, nothing to hand over.
          ${plymouth} --ping || exit 0
          # --wait returns once plymouthd has exited and let go of the display.
          if ! ${plymouth} quit --retain-splash --wait; then
            echo "handoff-plymouth-to-greetd: plymouth quit failed, starting the greeter anyway" >&2
          fi
        '';

      systemd.services.plymouth-poweroff = {
        after = [
          "display-manager.service"
          "greetd.service"
          "plymouth-start.service"
        ];
        before = [ "systemd-poweroff.service" ];
      };

      systemd.services.plymouth-reboot = {
        after = [
          "display-manager.service"
          "greetd.service"
          "plymouth-start.service"
        ];
        before = [ "systemd-reboot.service" ];
      };

      systemd.services.plymouth-halt = {
        after = [
          "display-manager.service"
          "greetd.service"
          "plymouth-start.service"
        ];
        before = [ "systemd-halt.service" ];
      };

      systemd.services.plymouth-kexec = {
        after = [
          "display-manager.service"
          "greetd.service"
          "plymouth-start.service"
        ];
        before = [ "systemd-kexec.service" ];
      };

      programs.nh = {
        enable = true;
        clean.enable = false;
        flake = settings.dotfilesDir;
      };
      environment.systemPackages = with pkgs; [
        gh
        go_1_26
        usbutils
        nixfmt
        python3
        nix-output-monitor
        nvd
        lm_sensors
        procps
        gawk
        findutils
        power-profiles-daemon
      ];
      # `gh auth setup-git` writes its own path into ~/.gitconfig. Without this
      # it writes a /nix/store path that a garbage collection later removes,
      # and git falls back to password prompts GitHub no longer accepts.
      environment.sessionVariables.GH_PATH = "/run/current-system/sw/bin/gh";
    }
    (lib.mkIf diagnosticsEnabled {
      home-manager.sharedModules = [ homeDiagnosticsModule ];
      systemd.services.boot-diagnostics = {
        description = "Boot diagnostics";
        wantedBy = [ "multi-user.target" ];
        after = [
          "NetworkManager.service"
          "display-manager.service"
          "home-manager-${settings.username}.service"
        ];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = bootDiagnostic;
          StandardOutput = "journal+console";
          StandardError = "journal+console";
          UMask = "0027";
        };
      };
      systemd.services.gjallar-pressure-monitor = {
        description = "Log what stalls the system (diagnostics)";
        wantedBy = [ "multi-user.target" ];
        serviceConfig = {
          ExecStart = pressureMonitor;
          Restart = "on-failure";
          ReadWritePaths = [ "/var/lib/gjallar-diagnostics" ];
          UMask = "0027";
          Nice = 10;
          IOSchedulingClass = "best-effort";
          IOSchedulingPriority = 7;
          # Reading other users' /proc/<pid>/io needs ptrace read access.
          CapabilityBoundingSet = [
            "CAP_SYS_PTRACE"
            "CAP_DAC_READ_SEARCH"
          ];
          NoNewPrivileges = true;
          ProtectSystem = "strict";
          ProtectHome = "read-only";
          PrivateTmp = true;
          PrivateNetwork = true;
          RestrictAddressFamilies = [ "AF_UNIX" ];
          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectControlGroups = true;
          MemoryMax = "64M";
        };
      };
      # Logs copy journal lines, so they get the journal's readers: root and
      # wheel. The setgid bit gives new files the wheel group.
      systemd.tmpfiles.rules = [ "d /var/lib/gjallar-diagnostics 2750 root wheel 30d" ];
    })
  ];
}
