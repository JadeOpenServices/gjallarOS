{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  batteryAvailable = lib.attrByPath [
    "class"
    "capabilities"
    "battery"
  ] false config.oddc.resolved;

  chargeThresholdsSupported = lib.attrByPath [
    "capabilities"
    "chargeThresholds"
  ] false config.oddc.resolved;

  chargeThresholdPolicy = lib.attrByPath [
    "class"
    "policy"
    "power"
    "chargeThresholds"
  ] { } config.oddc.resolved;

  chargeStartPercent = chargeThresholdPolicy.startPercent or null;
  chargeEndPercent = chargeThresholdPolicy.endPercent or null;
  chargeStartValue = if chargeStartPercent == null then 0 else chargeStartPercent;
  chargeEndValue = if chargeEndPercent == null then 100 else chargeEndPercent;

  batteryProtection = lib.attrByPath [
    "class"
    "policy"
    "power"
    "batteryProtection"
  ] { } config.oddc.resolved;

  lowWarningPercent = batteryProtection.lowWarningPercent or null;
  criticalWarningPercent = batteryProtection.criticalWarningPercent or null;
  shutdownPercent = batteryProtection.shutdownPercent or null;

  lowWarningValue = if lowWarningPercent == null then 10 else lowWarningPercent;
  criticalWarningValue = if criticalWarningPercent == null then 5 else criticalWarningPercent;
  shutdownValue = if shutdownPercent == null then 2 else shutdownPercent;

  batteryCountdown = pkgs.writeShellApplication {
    name = "gjallar-battery-countdown";
    runtimeInputs = [ pkgs.zenity ];
    text = ''
      set -euo pipefail

      produce_status() {
        while :; do
          capacity=100
          status=Unknown
          found=0

          for battery in /sys/class/power_supply/BAT*; do
            [ -r "$battery/capacity" ] || continue
            found=1
            value="$(cat "$battery/capacity")"
            [ "$value" -lt "$capacity" ] && capacity="$value"
            [ -r "$battery/status" ] && status="$(cat "$battery/status")"
          done

          [ "$found" -eq 1 ] || return 0
          [ "$status" = Discharging ] || return 0
          [ "$capacity" -gt ${toString shutdownValue} ] || return 0
          [ "$capacity" -le ${toString criticalWarningValue} ] || return 0

          progress=$(( (capacity - ${toString shutdownValue}) * 100 / (${toString criticalWarningValue} - ${toString shutdownValue}) ))
          printf '%s\n' "$progress"
          printf '# Battery: %s%%\nEmergency shutdown at ${toString shutdownValue}%%. Connect power now.\n' "$capacity"
          sleep 10
        done
      }

      produce_status | zenity --progress \
        --title="GjallarOS Battery" \
        --width=520 \
        --no-cancel \
        --auto-close \
        --percentage=100
    '';
  };

  batteryGuard = pkgs.writeShellApplication {
    name = "gjallar-battery-guard";
    runtimeInputs = with pkgs; [
      coreutils
      plymouth
      systemd
      util-linux
      zenity
    ];
    text = ''
      set -euo pipefail

      warned10=0
      warned5=0

      launch_warning() {
        level="$1"
        message="$2"
        runtime="/run/user/$(id -u ${lib.escapeShellArg settings.username})"
        [ -S "$runtime/bus" ] || return 0

        unit="battery-notification-$level-$(date +%s%N)"
        runuser -u ${lib.escapeShellArg settings.username} -- env \
          XDG_RUNTIME_DIR="$runtime" \
          DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
          systemd-run --user --quiet --collect --unit="$unit" \
            zenity --warning \
              --title="GjallarOS Battery" \
              --width=520 \
              --text="$message" \
              >/dev/null 2>&1 || true
      }

      launch_countdown() {
        runtime="/run/user/$(id -u ${lib.escapeShellArg settings.username})"
        [ -S "$runtime/bus" ] || return 0

        runuser -u ${lib.escapeShellArg settings.username} -- env \
          XDG_RUNTIME_DIR="$runtime" \
          DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
          systemd-run --user --quiet --collect \
            --unit=battery-shutdown-countdown \
            ${batteryCountdown}/bin/gjallar-battery-countdown \
            >/dev/null 2>&1 || true
      }

      close_warnings() {
        runtime="/run/user/$(id -u ${lib.escapeShellArg settings.username})"
        [ -S "$runtime/bus" ] || return 0

        runuser -u ${lib.escapeShellArg settings.username} -- env \
          XDG_RUNTIME_DIR="$runtime" \
          DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
          systemctl --user stop 'battery-notification-*' battery-shutdown-countdown \
            >/dev/null 2>&1 || true
      }

      while :; do
        capacity=100
        status=Unknown
        found=0

        for battery in /sys/class/power_supply/BAT*; do
          [ -r "$battery/capacity" ] || continue
          found=1
          value="$(cat "$battery/capacity")"
          [ "$value" -lt "$capacity" ] && capacity="$value"
          [ -r "$battery/status" ] && status="$(cat "$battery/status")"
        done

        if [ "$found" -eq 0 ] || [ "$status" != Discharging ]; then
          # Power came back while a warning was open: take the dialogs down.
          if [ "$warned10" -eq 1 ] || [ "$warned5" -eq 1 ]; then
            close_warnings
          fi
          warned10=0
          warned5=0
          sleep 20
          continue
        fi

        if [ "$capacity" -le ${toString shutdownValue} ]; then
          plymouth --show-splash >/dev/null 2>&1 || true
          plymouth change-mode --shutdown >/dev/null 2>&1 || true
          systemctl poweroff
          exit 0
        fi

        if [ "$capacity" -le ${toString criticalWarningValue} ] && [ "$warned5" -eq 0 ]; then
          warned5=1
          launch_warning critical \
            "<b>Critical battery: $capacity%</b>\n\nGjallarOS will shut down automatically at ${toString shutdownValue}% to protect the battery. Connect power now."
          launch_countdown
        elif [ "$capacity" -le ${toString lowWarningValue} ] && [ "$warned10" -eq 0 ]; then
          warned10=1
          launch_warning low \
            "<b>Low battery: $capacity%</b>\n\nConnect power soon. A shutdown countdown starts at ${toString criticalWarningValue}%, with emergency shutdown at ${toString shutdownValue}%."
        fi

        # Poll faster while a warning is on screen so it closes soon after
        # the charger is plugged in.
        if [ "$warned10" -eq 1 ] || [ "$warned5" -eq 1 ]; then
          sleep 2
        else
          sleep 20
        fi
      done
    '';
  };
in
{
  # UPower also reports mouse and headset batteries, so it runs on every
  # machine; the battery protection below stays with the ODDC capability.
  config = lib.mkMerge [
    { services.upower.enable = true; }
    (lib.mkIf batteryAvailable (lib.mkMerge [
    {
      assertions = [
        {
          assertion =
            shutdownPercent != null
            && criticalWarningPercent != null
            && lowWarningPercent != null
            && shutdownPercent >= 0
            && shutdownPercent < criticalWarningPercent
            && criticalWarningPercent < lowWarningPercent
            && lowWarningPercent <= 100;
          message = "ODDC battery-protection policy requires 0 <= shutdownPercent < criticalWarningPercent < lowWarningPercent <= 100.";
        }
      ];

      powerManagement.enable = true;

      systemd.services.battery-guard = {
        description = "Low-battery warnings and emergency shutdown";
        wantedBy = [ "multi-user.target" ];
        after = [
          "systemd-logind.service"
          "upower.service"
        ];
        serviceConfig = {
          ExecStart = lib.getExe batteryGuard;
          Restart = "always";
          RestartSec = 5;
          StandardOutput = "journal";
          StandardError = "journal";
        };
      };
    }

    (lib.mkIf chargeThresholdsSupported {
      assertions = [
        {
          assertion =
            chargeStartPercent != null
            && chargeEndPercent != null
            && chargeStartPercent >= 0
            && chargeStartPercent < chargeEndPercent
            && chargeEndPercent <= 100;
          message = "ODDC charge-threshold policy requires 0 <= startPercent < endPercent <= 100.";
        }
      ];
      systemd.services.battery-charge-thresholds = {
        description = "Apply battery charge thresholds";
        wantedBy = [ "multi-user.target" ];
        after = [ "local-fs.target" ];
        serviceConfig.Type = "oneshot";
        script = ''
          start_applied=0
          end_applied=0

          for battery in /sys/class/power_supply/BAT*; do
            [ -d "$battery" ] || continue

            if [ -w "$battery/charge_control_start_threshold" ]; then
              printf '%s\n' ${toString chargeStartValue} > "$battery/charge_control_start_threshold"
              start_applied=1
            elif [ -w "$battery/charge_start_threshold" ]; then
              printf '%s\n' ${toString chargeStartValue} > "$battery/charge_start_threshold"
              start_applied=1
            fi

            if [ -w "$battery/charge_control_end_threshold" ]; then
              printf '%s\n' ${toString chargeEndValue} > "$battery/charge_control_end_threshold"
              end_applied=1
            elif [ -w "$battery/charge_stop_threshold" ]; then
              printf '%s\n' ${toString chargeEndValue} > "$battery/charge_stop_threshold"
              end_applied=1
            fi
          done

          if [ "$start_applied" -ne 1 ] || [ "$end_applied" -ne 1 ]; then
            echo "ODDC declares charge-threshold support, but writable start/end controls are unavailable" >&2
            exit 1
          fi
        '';
      };
    })
    ]))
  ];
}
