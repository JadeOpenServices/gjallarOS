{
  config,
  pkgs,
  sourceRevision,
}:
let
  gjallarctl = pkgs.callPackage ../../pkgs/gjallarctl { };
  installer = pkgs.callPackage ../../pkgs/gjallar-installer { };
  recoveryInstall = pkgs.callPackage ../../pkgs/gjallar-recovery-install { };
  recoveryExecutor = pkgs.writeShellApplication {
    name = "gjallar-recovery-execute";
    runtimeInputs = with pkgs; [
      coreutils
      gawk
      gptfdisk
      jq
      util-linux
    ];
    text = builtins.readFile ../../scripts/recovery/execute-contract.sh;
  };

  recovery = pkgs.writeShellApplication {
    name = "gjallar-recover";
    runtimeInputs = with pkgs; [
      config.nix.package
      coreutils
      cryptsetup
      gawk
      git
      nixos-install-tools
      sbctl
      systemd
      tpm2-tools
      util-linux
      gjallarctl
      installer
    ];
    text = ''
      set -euo pipefail

      usage() {
        printf '%s\n' \
          'GjallarOS trusted recovery' \
          '  gjallar-recover audit' \
          '  gjallar-recover unlock DEVICE NAME' \
          '  gjallar-recover mount DEVICE MOUNTPOINT' \
          '  gjallar-recover open-root DEVICE [NAME]' \
          '  gjallar-recover generations ROOT' \
          '  gjallar-recover rollback ROOT GENERATION' \
          '  gjallar-recover repair-boot ROOT' \
          '  gjallar-recover rebuild ROOT FLAKE#HOST' \
          '  gjallar-recover developer-off' \
          '  gjallar-recover jods {repair|reinstall|fresh}' \
          '  gjallar-recover install'
      }

      require_root() {
        if [ "$(id -u)" -ne 0 ]; then
          printf '%s\n' 'ERROR: run this operation as root.' >&2
          exit 1
        fi
      }

      confirm_phrase() {
        expected="$1"
        printf 'Type %s to continue: ' "$expected"
        read -r answer
        [ "$answer" = "$expected" ] || {
          printf '%s\n' 'Cancelled.' >&2
          exit 1
        }
      }

      prepare_installer_device_config() {
        live_repo=/run/gjallarOS/repo
        runtime_config=/run/gjallarOS/device-config/user.config.json
        fallback_config=/etc/gjallar/installer-fallback-user.config.json
        target_config="$live_repo/user.config.json"

        [ -f "$live_repo/flake.nix" ] || return 0

        ${pkgs.coreutils}/bin/rm -f "$target_config"

        if [ -e "$runtime_config" ]; then
          if [ ! -s "$runtime_config" ]; then
            printf '%s\n' \
              'ERROR: runtime device configuration exists but is empty; refusing embedded fallback.' >&2
            return 1
          fi

          ${pkgs.coreutils}/bin/install \
            -m 0600 \
            "$runtime_config" \
            "$target_config"

          printf '%s\n' \
            'Installer configuration source: runtime device configuration.'
          return 0
        fi

        if [ -s "$fallback_config" ]; then
          ${pkgs.coreutils}/bin/install \
            -m 0600 \
            "$fallback_config" \
            "$target_config"

          printf '%s\n' \
            'Installer configuration source: embedded device configuration.'
          return 0
        fi

        printf '%s\n' \
          'Installer configuration source: interactive fallback.'
      }

      run_installer() {
        live_repo=/run/gjallarOS/repo

        if [ -f "$live_repo/flake.nix" ]; then
          prepare_installer_device_config || return 1
          GJALLAROS_REVISION=${sourceRevision} exec gjallar-installer --repo "$live_repo" "$@"
        fi

        GJALLAROS_REVISION=${sourceRevision} exec gjallar-installer "$@"
      }

      case "''${1:-help}" in
        audit)
          printf '%s\n' '=== Secure Boot ==='
          sbctl status || true
          printf '%s\n' '=== TPM ==='
          systemd-analyze has-tpm2 || true
          tpm2_getcap properties-fixed || true
          printf '%s\n' '=== disks ==='
          lsblk -o NAME,PATH,TYPE,FSTYPE,UUID,PARTUUID,MOUNTPOINTS
          ;;
        unlock)
          require_root
          device="''${2:?DEVICE required}"
          name="''${3:?mapping NAME required}"
          cryptsetup isLuks "$device"
          systemd-ask-password -n 'GjallarOS: enter the recovery key or disk passphrase:' |
            cryptsetup open --type luks --key-file=- "$device" "$name"
          ;;
        mount)
          require_root
          device="''${2:?DEVICE required}"
          target="''${3:?MOUNTPOINT required}"
          mkdir -p "$target"
          mount "$device" "$target"
          ;;
        open-root)
          require_root

          device="''${2:?encrypted root DEVICE required}"
          name="''${3:-gjallar-recovery-root}"
          target=/mnt
          mapping="/dev/mapper/$name"

          cryptsetup isLuks "$device" || {
            printf '%s\n'               "ERROR: $device is not a LUKS device." >&2
            exit 1
          }

          if findmnt -rn --mountpoint "$target" >/dev/null 2>&1; then
            printf '%s\n'               "ERROR: $target is already mounted; refusing an ambiguous recovery root." >&2
            exit 1
          fi

          if [ -e "$mapping" ]; then
            printf '%s\n'               "ERROR: mapper $mapping already exists; refusing to reuse an unauthenticated mapping." >&2
            exit 1
          fi

          printf '%s\n'             'GjallarOS installed root is encrypted.'             'Authentication is required before its files can be viewed or modified.'

          systemd-ask-password -n             'GjallarOS recovery: enter the recovery key or disk passphrase:' |
            cryptsetup open               --type luks               --key-file=-               "$device"               "$name"

          cleanup_mapping=true

          cleanup_open_root() {
            if [ "$cleanup_mapping" = true ] && [ -e "$mapping" ]; then
              cryptsetup close "$name" >/dev/null 2>&1 || true
            fi
          }

          trap cleanup_open_root EXIT

          mkdir -p "$target"

          if ! mount -o rw "$mapping" "$target"; then
            printf '%s\n'               'ERROR: LUKS authentication succeeded but the installed root could not be mounted.' >&2
            exit 1
          fi

          source="$(
            findmnt -nro SOURCE --mountpoint "$target"
          )"

          options="$(
            findmnt -nro OPTIONS --mountpoint "$target"
          )"

          if [ "$source" != "$mapping" ]; then
            umount "$target" >/dev/null 2>&1 || true
            printf '%s\n'               "ERROR: $target resolved to unexpected source $source; expected $mapping." >&2
            exit 1
          fi

          case ",$options," in
            *,rw,*)
              ;;
            *)
              umount "$target" >/dev/null 2>&1 || true
              printf '%s\n'                 "ERROR: installed root was not mounted read-write at $target." >&2
              exit 1
              ;;
          esac

          cleanup_mapping=false
          trap - EXIT

          printf '%s\n'             'PASS: recovery authorization accepted.'             "PASS: installed GjallarOS root unlocked as $mapping."             "PASS: installed GjallarOS root mounted read-write at $target."             'Repair tools may now operate against /mnt.'
          ;;
        generations)
          root="''${2:?installed ROOT mountpoint required}"
          nix-env --list-generations --profile "$root/nix/var/nix/profiles/system"
          ;;
        rollback)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          generation="''${3:?GENERATION required, see gjallar-recover generations ROOT}"
          [ -e "$root/etc/NIXOS" ] || {
            printf '%s\n' 'ERROR: target is not a mounted NixOS installation.' >&2
            exit 1
          }
          case "$generation" in
            ""|*[!0-9]*) printf '%s\n' 'ERROR: GENERATION must be a number.' >&2; exit 1 ;;
          esac
          toplevel="$(readlink "$root/nix/var/nix/profiles/system-$generation-link")" || {
            printf '%s\n' "ERROR: generation $generation does not exist." >&2
            exit 1
          }
          confirm_phrase ROLLBACK
          # A new generation with the old system: the boot menu lists only
          # the newest one, and nothing newer is deleted.
          switch="/nix/var/nix/profiles/system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set $toplevel && /nix/var/nix/profiles/system/bin/switch-to-configuration boot"
          if [ "$(realpath "$root")" = / ]; then
            sh -c "$switch"
          else
            nixos-enter --root "$root" -c "$switch"
          fi
          printf '%s\n' "PASS: generation $generation is the next boot."
          ;;
        repair-boot)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          [ -e "$root/etc/NIXOS" ] || {
            printf '%s\n' 'ERROR: target is not a mounted NixOS installation.' >&2
            exit 1
          }
          # open-root mounts only the root; without the ESP, bootctl would
          # write into the root's empty /boot (real HP, 2026-10-08).
          if ! findmnt -rn --mountpoint "$root/boot" >/dev/null 2>&1; then
            esp="$(awk '$2 == "/boot" { print $1 }' "$root/etc/fstab")"
            [ -n "$esp" ] && [ "$(printf '%s\n' "$esp" | wc -l)" -eq 1 ] || {
              printf '%s\n' 'ERROR: installed fstab must define exactly one /boot.' >&2
              exit 1
            }
            mkdir -p "$root/boot"
            mount "$esp" "$root/boot"
          fi
          confirm_phrase REPAIR-BOOT
          NIXOS_INSTALL_BOOTLOADER=1 nixos-enter --root "$root" -- \
            /run/current-system/bin/switch-to-configuration boot
          # bootctl appends a restored entry behind USB, PXE and recovery.
          ${recoveryInstall}/bin/gjallar-boot-first "$root/boot"
          ;;
        rebuild)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          flake="''${3:?FLAKE#HOST required}"
          [ -e "$root/etc/NIXOS" ] || {
            printf '%s\n' 'ERROR: target is not a mounted NixOS installation.' >&2
            exit 1
          }
          confirm_phrase REBUILD
          case "$flake" in
            /*#*|path:/*#*) ;;
            *) printf '%s\n' 'ERROR: recovery rebuild requires an absolute local repository path followed by #HOST.' >&2; exit 1 ;;
          esac
          repo="''${flake%#*}"
          repo="''${repo#path:}"
          host="''${flake##*#}"
          nixos-enter --root "$root" -- /run/current-system/sw/bin/gjallarctl \
            installer deploy --repo "$repo" --hostname "$host" --apply
          ;;
        developer-off)
          # The way back when local modules broke the system (ROADMAP
          # section 14). This entry never holds them, so its gjallarctl
          # and rebuild are the official ones.
          require_root
          [ -e /etc/gjallar/recovery ] || {
            printf '%s\n' \
              'ERROR: run this in the trusted recovery boot entry.' \
              'In the running system: gjallarctl settings set developerMode=false' >&2
            exit 1
          }
          printf '%s\n' \
            'This turns developer mode off and builds the official system for the next boot.' \
            'Your ~/GjallarOS-local folder is kept.'
          confirm_phrase DEVELOPER-OFF
          # It prints PASS only when the next boot holds no local files.
          exec gjallarctl system developer-off
          ;;
        jods-execute)
          require_root
          contract="''${2:?CONTRACT required}"
          artifact="''${3:?ARTIFACT required}"
          exec gjallar-recovery-execute "$contract" "$artifact"
          ;;
        jods)
          mode="''${2:-}"
          case "$mode" in
            repair)
              printf '%s\n' \
                'JODS mode: REPAIR EXISTING INSTALLATION' \
                'Disk formatting is forbidden. Unlock with a human recovery credential, then use repair-boot or rebuild.'
              ;;
            reinstall)
              printf '%s\n' \
                'JODS mode: REINSTALL EXISTING INSTALLATION' \
                'Existing partitions and encrypted data must be detected before any installer runs.'
              confirm_phrase REINSTALL-EXISTING
              run_installer --recovery --accept-existing
              ;;
            fresh)
              printf '%s\n' \
                'DANGER: JODS FRESH INSTALL MODE' \
                'Select the target disk in the installer and confirm its destructive plan.'
              confirm_phrase ERASE-FOR-FRESH-INSTALL
              run_installer --recovery
              ;;
            *)
              printf '%s\n' 'Usage: gjallar-recover jods {repair|reinstall|fresh}' >&2
              exit 2
              ;;
          esac
          ;;
        install)
          # The recovery console runs this after it erased the disk.
          run_installer --recovery
          ;;
        *) usage ;;
      esac
    '';
  };

  recoveryConsole = pkgs.writeShellApplication {
    name = "gjallar-recovery-console";
    runtimeInputs = with pkgs; [
      bashInteractive
      coreutils
      cryptsetup
      systemd
      util-linux
      recovery
    ];
    # A failed command returns to the menu instead of ending the console.
    bashOptions = [
      "nounset"
      "pipefail"
    ];
    text = builtins.readFile ../../scripts/recovery/console.sh;
  };
in
{
  inherit
    gjallarctl
    installer
    recovery
    recoveryConsole
    recoveryExecutor
    ;
}
