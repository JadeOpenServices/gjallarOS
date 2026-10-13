{
  lib,
  pkgs,
  gjallarctlPackage,
  settings,
  gjallarSecureBootArtifactVerifier,
  gjallarSecureBootOwnershipVerifier,
  ...
}:
{
  systemd.services.secure-boot-enrollment = lib.mkIf settings.secureBootEnable {
    description = "Apply trusted Secure Boot firmware policy";
    wantedBy = [ "multi-user.target" ];
    after = [ "local-fs.target" ];
    unitConfig.ConditionPathExists = "/var/lib/gjallarOS/secure-boot-enrollment-armed";

    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
    };

    path = [
      gjallarctlPackage
      pkgs.sbctl
      # sign-efi-sig-list and efi-updatevar put an emptied dbx back from
      # dbxDefault; sbctl cannot write dbx.
      pkgs.efitools
      # sbctl verify locates the ESP with lsblk (e2e-target, 2026-09-29).
      pkgs.util-linux
      pkgs.coreutils
      pkgs.e2fsprogs
      pkgs.gnugrep
      pkgs.systemd
    ];

    script = ''
      set -euo pipefail

      gjallarctl installer secure-boot-enroll

      if [ -e /var/lib/gjallarOS/secure-boot-enable-required ]; then
        sync
        systemctl reboot --firmware-setup
      fi
    '';
  };

  systemd.services.secure-boot-verification = lib.mkIf settings.secureBootEnable {
    description = "Verify final Secure Boot activation";
    wantedBy = [ "multi-user.target" ];
    after = [
      "local-fs.target"
      "systemd-remount-fs.service"
    ];
    unitConfig.ConditionPathExists = "/var/lib/gjallarOS/secure-boot-enable-required";

    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
    };

    path = [
      pkgs.sbctl
      pkgs.coreutils
      pkgs.gnugrep
      gjallarSecureBootArtifactVerifier
      gjallarSecureBootOwnershipVerifier
    ];

    script = ''
              set -euo pipefail
              export LC_ALL=C

              marker=/var/lib/gjallarOS/secure-boot-enable-required

              if ! status="$(sbctl status 2>&1)"; then
                printf '%s\n' 'ERROR: sbctl status failed during boot.' "$status" >&2
                exit 1
              fi

              if ! printf '%s\n' "$status" | grep -Eq 'Secure Boot:.*Enabled'; then
                printf '%s\n' \
                  'GjallarOS Secure Boot keys are enrolled, but Secure Boot enforcement is still disabled.' \
                  'Enter firmware setup and enable Secure Boot.' >&2
                exit 0
              fi

              if printf '%s\n' "$status" | grep -Eq 'Setup Mode:.*Enabled'; then
                printf '%s\n' \
                  'ERROR: Secure Boot reports enabled while firmware remains in Setup Mode.' >&2
                exit 1
              fi

              gjallar-verify-secure-boot-artifacts
              gjallar-verify-secure-boot-ownership enrolled

              rm -f -- "$marker"

              printf '%s\n' \
                'GjallarOS Secure Boot provisioning complete.' \
                'Secure Boot is enabled, Setup Mode is disabled, and signed artifacts verified.'
    '';
  };

  systemd.services.installer-post-secure-boot = lib.mkIf settings.secureBootEnable {
    description = "Finish installer after Secure Boot provisioning";

    wantedBy = [ "multi-user.target" ];

    # Enrollment decides whether this boot continues at all: started in
    # parallel, this unit told the user to remove PK while firmware already
    # was in Setup Mode (e2e-target, 2026-09-29).
    after = [
      "local-fs.target"
      "systemd-remount-fs.service"
      "secure-boot-enrollment.service"
      "secure-boot-verification.service"
    ]
    ++ lib.optional settings.luksTpm2Enable "measured-boot-tpm2-enrollment.service";

    requires = lib.optional settings.luksTpm2Enable "measured-boot-tpm2-enrollment.service";

    unitConfig.ConditionPathExists = "/var/lib/gjallarOS/installer-resume-after-secure-boot";

    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
    };

    path = [
      pkgs.sbctl
      gjallarctlPackage
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gawk
      pkgs.systemd
      pkgs.util-linux
      pkgs.zenity
      gjallarSecureBootArtifactVerifier
      gjallarSecureBootOwnershipVerifier
    ];

    script = ''
                      set -euo pipefail
                      export LC_ALL=C

                      resume=/var/lib/gjallarOS/installer-resume-after-secure-boot
                      sb_enrollment=/var/lib/gjallarOS/secure-boot-enrollment-armed
                      sb_final=/var/lib/gjallarOS/secure-boot-enable-required
                      status_file=/var/lib/gjallarOS/installer-status.txt
                      complete=/var/lib/gjallarOS/installation-complete

                      write_status() {
                        message="$1"

                        install -d -m 0700 /var/lib/gjallarOS

                        printf '%s\n' "$message" > "$status_file"
                        chmod 0644 "$status_file"

                        printf '%s\n' "$message"

                        printf '\n[GjallarOS Installer]\n%s\n\n' "$message" |
                          wall 2>/dev/null || true
                      }

                      gtk_message() {
                        message="$1"
                        timeout_seconds="''${2:-0}"
                        gtk_log=/var/lib/gjallarOS/installer-gtk.log
                        launched=0

                        for runtime in /run/user/[0-9]*; do
                          [ -d "$runtime" ] || continue
                          [ -S "$runtime/bus" ] || continue

                          uid="''${runtime##*/}"

                          user="$(
                            loginctl list-users --no-legend 2>/dev/null |
                              awk -v uid="$uid" '$1 == uid { print $2; exit }'
                          )"

                          [ -n "$user" ] || continue

                          unit="installer-notice-$(date +%s%N)"

                          if [ "$timeout_seconds" -gt 0 ]; then
                            if runuser -u "$user" -- \
                              env \
                                XDG_RUNTIME_DIR="$runtime" \
                                DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
                              systemd-run \
                                --user \
                                --quiet \
                                --collect \
                                --unit="$unit" \
                                ${lib.getExe pkgs.zenity} \
                                  --info \
                                  --timeout="$timeout_seconds" \
                                  --title="GjallarOS Installer" \
                                  --width=520 \
                                  --text="$message"
                            then
                              printf '%s
              ' \
                                "GTK installer notification launched for $user ($unit)." \
                                >> "$gtk_log"
                              launched=1
                            else
                              rc=$?
                              printf '%s
              ' \
                                "GTK installer notification FAILED for $user (rc=$rc)." \
                                >> "$gtk_log"
                            fi
                          else
                            if runuser -u "$user" -- \
                              env \
                                XDG_RUNTIME_DIR="$runtime" \
                                DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" \
                              systemd-run \
                                --user \
                                --quiet \
                                --collect \
                                --unit="$unit" \
                                ${lib.getExe pkgs.zenity} \
                                  --info \
                                  --title="GjallarOS Installer" \
                                  --width=520 \
                                  --text="$message"
                            then
                              printf '%s
              ' \
                                "GTK installer notification launched for $user ($unit)." \
                                >> "$gtk_log"
                              launched=1
                            else
                              rc=$?
                              printf '%s
              ' \
                                "GTK installer notification FAILED for $user (rc=$rc)." \
                                >> "$gtk_log"
                            fi
                          fi

                          break
                        done

                        if [ "$launched" -eq 0 ]; then
                          printf '%s
              ' \
                            'No usable graphical user session found; terminal/journal status remains available.' \
                            >> "$gtk_log"
                        fi

                        return 0
                      }

                      pause_visible() {
                        message="$1"

                        write_status "INSTALLER PAUSED: $message"
                        gtk_message "GjallarOS installation is paused.

              $message

              The continuation marker was kept so the operation can be resumed safely."

                        exit 0
                      }

                      write_status \
                        "GjallarOS is checking the pending Secure Boot installation stage."

                      if [ -e "$sb_enrollment" ]; then
                        instructions="$(
                          gjallarctl installer secure-boot-firmware-instructions
                        )" || pause_visible \
                          "Trusted Secure Boot firmware instructions could not be loaded."

                        pause_visible "Firmware action required:

      $instructions

      After completing those firmware steps, boot GjallarOS again."
                      fi

                      if [ -e "$sb_final" ]; then
                        pause_visible \
                          "GjallarOS keys are enrolled. Enable Secure Boot in firmware, save, then boot GjallarOS for final verification. Do not clear or replace PK, KEK, DB or DBX."
                      fi

                      status="$(sbctl status 2>&1)" ||
                        pause_visible "Unable to read Secure Boot status."

                      printf '%s\n' "$status" |
                        grep -Eq 'Secure Boot:.*Enabled' ||
                        pause_visible "Secure Boot is not enforcing."

                      if printf '%s\n' "$status" |
                        grep -Eq 'Setup Mode:.*Enabled'; then
                        pause_visible "Firmware is unexpectedly still in Secure Boot Setup Mode."
                      fi

                      write_status "Verifying signed GjallarOS boot artifacts..."

                      gjallar-verify-secure-boot-artifacts ||
                        pause_visible "GjallarOS boot artifact verification failed."

                      write_status "Verifying GjallarOS Secure Boot ownership..."

                      gjallar-verify-secure-boot-ownership enrolled ||
                        pause_visible "GjallarOS PK/KEK/db ownership verification failed."

                      [ -e /run/current-system ] ||
                        pause_visible "/run/current-system is missing."

                      [ -e /run/booted-system ] ||
                        pause_visible "/run/booted-system is missing."

                      write_status "Final GjallarOS installer checks passed."

                      install -m 0600 /dev/null "$complete"
                      rm -f -- "$resume"

                      sync

                      write_status "GjallarOS installation complete."

                      gtk_message "GjallarOS installation complete.

              Secure Boot is enabled.
              GjallarOS PK/KEK/db ownership is verified.
              Boot artifacts are verified.

              The installation transaction has finished successfully."
    '';
  };

}
