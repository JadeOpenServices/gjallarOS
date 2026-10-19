{
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
      if ${coreutils}/test -L "$home/.config/hypr/hyprland.conf"; then
        log "hyprland-config=$(${coreutils}/readlink -f "$home/.config/hypr/hyprland.conf")"
      else
        log "hyprland-config missing: $home/.config/hypr/hyprland.conf"
      fi
    }

    log 'begin'
    ${coreutils}/date --iso-8601=seconds
    ${coreutils}/uname -a
    ${coreutils}/df -h / /boot || true
    ${systemctl} --failed --no-pager || true
    report_unit NetworkManager.service
    report_unit display-manager.service
    report_unit "home-manager-$(${systemdEscape} -- "${settings.username}").service"
    report_home "/home/${settings.username}"
    ${pkgs.networkmanager}/bin/nmcli general status || true
    ${pkgs.networkmanager}/bin/nmcli device status || true
    ${pkgs.systemd}/bin/journalctl -b -p warning..alert --no-pager || true
    log 'end'
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
        ${coreutils}/printenv | ${pkgs.gnugrep}/bin/grep -E '^(PATH|XDG_|WAYLAND_DISPLAY|HYPRLAND_INSTANCE_SIGNATURE|DBUS_SESSION_BUS_ADDRESS)=' || true
        # Hyprland opens its IPC socket during init and starts
        # hyprland-session.target later from exec-once, so one try is enough;
        # false here is a real finding.
        hyprland_ready=false
        if ${pkgs.hyprland}/bin/hyprctl monitors >/dev/null 2>&1; then
          hyprland_ready=true
        fi
        log "hyprland-ipc-ready=$hyprland_ready"
        for command in fuzzel ghostty noctalia; do
          path="$(${pkgs.findutils}/bin/find "$HOME/.nix-profile/bin" "/etc/profiles/per-user/$USER/bin" -maxdepth 1 -name "$command" \( -type l -o -type f \) -print -quit 2>/dev/null || true)"
          if [ -n "$path" ]; then log "command=$command path=$path"; else log "command=$command missing from user profiles"; fi
        done
        if ${coreutils}/test -L "$HOME/.config/hypr/hyprland.conf"; then
          log "hyprland-config=$(${coreutils}/readlink -f "$HOME/.config/hypr/hyprland.conf")"
        else
          log 'hyprland-config missing'
        fi
        ${pkgs.hyprland}/bin/hyprctl version || true
        ${pkgs.hyprland}/bin/hyprctl configerrors || true
        ${pkgs.hyprland}/bin/hyprctl monitors || true
        ${pkgs.hyprland}/bin/hyprctl clients || true
        ${lib.getExe config.programs.noctalia.package} config validate || true
        ${lib.getExe config.programs.noctalia.package} msg status >/dev/null 2>&1 || true
        ${pkgs.systemd}/bin/journalctl --user -b --no-pager -u hyprland-session-diagnostics.service || true
        ${pkgs.procps}/bin/pgrep -f -a -u "$USER" 'Hyprland|noctalia|fuzzel|ghostty|swaybg|waybar' || true
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

      systemd.services.plymouth-quit.enable = false;
      systemd.services.plymouth-quit-wait.enable = false;

      systemd.services.greetd.serviceConfig.ExecStartPre =
        pkgs.writeShellScript "handoff-plymouth-to-greetd" ''
          ${pkgs.plymouth}/bin/plymouth quit --retain-splash || true
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
          StateDirectory = "gjallar-diagnostics";
        };
      };
    })
  ];
}
