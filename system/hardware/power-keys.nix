{
  lib,
  pkgs,
  settings,
  ...
}:
let
  # Decided at every boot from what the machine is and can do, so a machine
  # without an ODDC model gets the same answer as one with it. A short press
  # suspends only on a portable machine (portable chassis or a system
  # battery) whose sleep works; everything else keeps logind's poweroff.
  # The lid follows the same answer and locks when suspend is out.
  powerKeys = pkgs.writeShellApplication {
    name = "gjallar-power-keys";
    runtimeInputs = [
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gnused
      pkgs.systemd
    ];
    text = ''
      chassis="$(cat /sys/class/dmi/id/chassis_type 2>/dev/null || true)"
      portable=no
      # SMBIOS chassis types: portable, laptop, notebook, sub-notebook,
      # tablet, convertible, detachable.
      case "$chassis" in
        8 | 9 | 10 | 14 | 30 | 31 | 32) portable=yes ;;
      esac

      battery=no
      for supply in /sys/class/power_supply/*; do
        [[ -r "$supply/type" && "$(cat "$supply/type")" == Battery ]] || continue
        # Mice, headsets and pens report scope Device; the system battery
        # reports System or nothing.
        [[ "$(cat "$supply/scope" 2>/dev/null || true)" == Device ]] && continue
        battery=yes
      done
      [[ $battery == yes ]] && portable=yes

      allow="$(systemd-analyze cat-config systemd/sleep.conf 2>/dev/null |
        sed -n 's/^[[:space:]]*AllowSuspend[[:space:]]*=[[:space:]]*//p' | tail -n 1)"
      sleep=no
      if grep -qw mem /sys/power/state 2>/dev/null &&
        [[ -n "$(cat /sys/power/mem_sleep 2>/dev/null || true)" ]]; then
        case "$allow" in
          no | false | off | 0) ;;
          *) sleep=yes ;;
        esac
      fi

      conf=/run/systemd/logind.conf.d/50-gjallar-power.conf
      mkdir -p "''${conf%/*}"
      if [[ $portable == yes && $sleep == yes ]]; then
        key=suspend lid=suspend
        printf '[Login]\nHandlePowerKey=suspend\nHandlePowerKeyLongPress=poweroff\nHandleLidSwitch=suspend\n' >"$conf.tmp"
      else
        key=poweroff lid=lock
        printf '[Login]\nHandlePowerKey=poweroff\nHandleLidSwitch=lock\n' >"$conf.tmp"
      fi
      mv "$conf.tmp" "$conf"
      echo "chassis=''${chassis:-unknown} battery=$battery sleep=$sleep: power key $key, lid $lid"

      # At boot this runs before logind; after a rebuild logind is already
      # up and rereads its config on reload.
      if systemctl is-active --quiet systemd-logind.service; then
        systemctl reload --no-block systemd-logind.service
      fi
    '';
  };
in
{
  systemd.services.gjallar-power-keys = {
    description = "Choose power key and lid actions from chassis, battery and sleep";
    wantedBy = [
      "multi-user.target"
      "systemd-logind.service"
    ];
    before = [ "systemd-logind.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = lib.getExe powerKeys;
    };
  };

  # Clamshell: an external screen keeps working with the lid shut while on
  # power or docked.
  services.logind.settings.Login = lib.mkIf settings.clamshellEnable {
    HandleLidSwitchExternalPower = "ignore";
    HandleLidSwitchDocked = "ignore";
  };
}
