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
      btrfs-progs
      coreutils
      cryptsetup
      gawk
      git
      gnugrep
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
          '  gjallar-recover snapshots ROOT' \
          '  gjallar-recover snapshot-rollback ROOT SNAPSHOT' \
          '  gjallar-recover snapshot-drop ROOT COPY' \
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

      # Snapshot rollbacks run only in recovery (ROADMAP section 15, step 4):
      # the recovery partition with the system at /mnt, or the trusted
      # recovery boot entry.
      recovery_only() {
        if [ "$(realpath "$1")" = / ] && [ ! -e /etc/gjallar/recovery ]; then
          printf '%s\n' 'ERROR: run this in the trusted recovery boot entry or the recovery partition.' >&2
          exit 1
        fi
      }

      # btrbk's long-iso names (system/services/snapshots.nix). A copy
      # replaced by a rollback has the same form without the _N.
      snapshot_name='^(home|config)\.[0-9]{8}T[0-9]{6}[+-][0-9]{4}(_[0-9]{1,4})?$'
      replaced_name='^(home|config)\.[0-9]{8}T[0-9]{6}[+-][0-9]{4}$'

      # Resolves PATH inside the installed system at ROOT. Its absolute links
      # point into its own /nix/store; followed as they are, they would land
      # in the recovery's files (/etc/fstab is one).
      installed_path() {
        root="$1"
        rest="''${2#/}"
        resolved=
        hops=0
        while [ -n "$rest" ]; do
          part="''${rest%%/*}"
          case "$rest" in
            */*) rest="''${rest#*/}" ;;
            *) rest= ;;
          esac
          case "$part" in
            "" | .) continue ;;
            ..)
              resolved="''${resolved%/*}"
              continue
              ;;
          esac
          if [ -L "$root$resolved/$part" ]; then
            hops=$((hops + 1))
            [ "$hops" -le 40 ] || return 1
            link="$(readlink "$root$resolved/$part")"
            case "$link" in
              /*) resolved= ;;
            esac
            rest="''${link#/}''${rest:+/$rest}"
          else
            resolved="$resolved/$part"
          fi
        done
        printf '%s\n' "$root$resolved"
      }

      # open-root mounts the system, not the ESP; without it, bootctl would
      # write into the root's empty /boot (real HP, 2026-10-08). The mount
      # options come from the installed fstab, as on a normal boot.
      mount_esp() {
        root="$1"
        if findmnt -rn --mountpoint "$root/boot" >/dev/null 2>&1; then
          return
        fi
        fstab="$(installed_path "$root" /etc/fstab)" && [ -f "$fstab" ] || {
          printf '%s\n' 'ERROR: the installed system has no readable /etc/fstab.' >&2
          exit 1
        }
        [ "$(awk '$1 !~ /^#/ && $2 == "/boot"' "$fstab" | wc -l)" -eq 1 ] || {
          printf '%s\n' 'ERROR: installed fstab must define exactly one /boot.' >&2
          exit 1
        }
        mkdir -p "$root/boot"
        mount --fstab "$fstab" --target-prefix "$root" /boot
      }

      # Mounts the top level of ROOT's Btrfs in a private folder for this
      # command; only the subvolume layout has snapshots.
      open_top() {
        root="$1"
        [ -e "$root/etc/NIXOS" ] || {
          printf '%s\n' 'ERROR: target is not a mounted NixOS installation.' >&2
          exit 1
        }
        if [ "$(findmnt -nro FSROOT --mountpoint "$root")" != /@ ]; then
          printf '%s\n' "ERROR: $root has no snapshots; only installs with the subvolume layout have them." >&2
          exit 1
        fi
        device="$(findmnt -nvro SOURCE --mountpoint "$root")"
        top="$(mktemp -d /run/gjallar-recover-top.XXXXXX)"
        trap close_top EXIT
        mount -o subvolid=5,nosuid,nodev,noexec "$device" "$top"
      }

      close_top() {
        umount "$top" 2>/dev/null || true
        rmdir "$top" 2>/dev/null || true
      }

      subvolume_mounted() {
        id="$(btrfs inspect-internal rootid "$1")"
        findmnt -rn -t btrfs -o OPTIONS | grep -Eq "(^|,)subvolid=$id(,|$)"
      }

      # A replaced copy that is still mounted stays writable until the
      # restart; the next recovery command makes it read-only.
      seal_replaced() {
        for copy in "$top"/@snapshots/replaced/*; do
          [ -d "$copy" ] || continue
          subvolume_mounted "$copy" || btrfs property set -ts "$copy" ro true
        done
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
          systemd-ask-password --timeout=0 -n 'GjallarOS: enter the recovery key or disk passphrase:' |
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

          # No timeout: a prompt that gave up while someone typed a long
          # recovery key left the rest of it on the screen (e2e-fw13,
          # 2026-10-28).
          systemd-ask-password --timeout=0 -n             'GjallarOS recovery: enter the recovery key or disk passphrase:' |
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
            findmnt -nvro SOURCE --mountpoint "$target"
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

          # Installs from before snapshots keep / in @ as well, but the top
          # level is their default subvolume.
          if [ "$(findmnt -nro FSROOT --mountpoint "$target")" = / ] &&
            [ ! -e "$target/etc/NIXOS" ] && [ -e "$target/@/etc/NIXOS" ]; then
            if ! umount "$target" || ! mount -o rw,subvol=@ "$mapping" "$target"; then
              umount "$target" >/dev/null 2>&1 || true
              printf '%s\n' "ERROR: subvolume @ could not be mounted at $target." >&2
              exit 1
            fi
          fi

          # / is on @ and the rest on their own subvolumes; generations and
          # nixos-enter need /nix. Installs from before snapshots (ROADMAP
          # section 15) have no @config; their settings are in @. The
          # snapshots stay unmounted.
          if [ "$(findmnt -nro FSROOT --mountpoint "$target")" = /@ ]; then
            subvolumes="$(btrfs subvolume list "$target" | awk '{ print $NF }')"
            for sub in @nix:nix @home:home @config:etc/gjallar/system; do
              printf '%s\n' "$subvolumes" | grep -qxF -- "''${sub%%:*}" || continue
              if ! mount -o rw,subvol="''${sub%%:*}" "$mapping" "$target/''${sub#*:}"; then
                umount -R "$target" >/dev/null 2>&1 || true
                printf '%s\n' "ERROR: subvolume ''${sub%%:*} could not be mounted at $target/''${sub#*:}." >&2
                exit 1
              fi
            done
          fi

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
          mount_esp "$root"
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
        snapshots)
          require_root
          open_top "''${2:?installed ROOT mountpoint required}"
          seal_replaced
          for kind in home config; do
            printf '%s\n' "=== $kind (snapshot-rollback ROOT NAME) ==="
            ls -1r "$top/@snapshots/$kind" 2>/dev/null || true
          done
          printf '%s\n' '=== replaced by a rollback (snapshot-drop ROOT NAME) ==='
          ls -1r "$top/@snapshots/replaced" 2>/dev/null || true
          ;;
        snapshot-rollback)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          name="''${3:?SNAPSHOT required, see gjallar-recover snapshots ROOT}"
          recovery_only "$root"
          [[ $name =~ $snapshot_name ]] || {
            printf '%s\n' "ERROR: $name is not a snapshot name; see gjallar-recover snapshots ROOT." >&2
            exit 1
          }
          kind="''${name%%.*}"
          case "$kind" in
            home) mountpoint=/home ;;
            config) mountpoint=/etc/gjallar/system ;;
          esac
          open_top "$root"
          snapshot="$top/@snapshots/$kind/$name"
          if [ "$(btrfs property get -ts "$snapshot" ro 2>/dev/null)" != ro=true ]; then
            printf '%s\n' "ERROR: there is no snapshot $name." >&2
            exit 1
          fi
          target="''${root%/}$mountpoint"
          stamp="$(date +%Y%m%dT%H%M%S%z)"
          replaced="$top/@snapshots/replaced/$kind.$stamp"
          printf '%s\n' "This puts $mountpoint back to the snapshot $name." \
            "What is there now is kept as $kind.$stamp (gjallar-recover snapshots ROOT)."
          # A snapshot never holds the subvolumes inside it.
          nested="$(btrfs subvolume list -o "$top/@$kind" | awk '{ print "  " $NF }')"
          if [ -n "$nested" ]; then
            printf '%s\n' 'These subvolumes are not in snapshots; they stay in the kept copy:' "$nested"
          fi
          confirm_phrase "ROLLBACK-''${kind^^}"
          if [ "$(realpath "$root")" = / ]; then
            # This boot's timers would snapshot the old copy again; the next
            # boot starts them.
            systemctl stop gjallar-snapshot-home.timer gjallar-snapshot-config.timer \
              gjallar-snapshot-home.service gjallar-snapshot-config.service 2>/dev/null || true
          fi
          mounted=false
          offline=true
          if findmnt -rn --mountpoint "$target" >/dev/null 2>&1; then
            mounted=true
            umount "$target" 2>/dev/null || offline=false
          fi
          mkdir -p "$top/@snapshots/replaced"
          chmod 0700 "$top/@snapshots/replaced"
          # A new writable snapshot; the read-only one stays as it is.
          btrfs subvolume snapshot "$snapshot" "$top/@$kind.new-$stamp" >/dev/null
          # One rename swaps both, so a power cut leaves either the old or
          # the new one at @$kind.
          mv --exchange --no-target-directory "$top/@$kind.new-$stamp" "$top/@$kind"
          mv --no-target-directory "$top/@$kind.new-$stamp" "$replaced"
          seal_replaced
          if [ "$offline" = true ]; then
            if [ "$mounted" = true ]; then
              if [ "$(realpath "$root")" = / ]; then
                mount "$target"
              else
                mount -o rw,subvol="@$kind" "$device" "$target"
              fi
            fi
            printf '%s\n' "PASS: $mountpoint is back to $name."
          else
            printf '%s\n' "PASS: $mountpoint is back to $name from the next start." \
              "$target is in use, so this boot still sees the old files; changes made now end up in the kept copy."
          fi
          if [ "$kind" = config ]; then
            printf '%s\n' 'The settings are back; the system built from them is not. Run rebuild once the system is up.'
          fi
          ;;
        snapshot-drop)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          name="''${3:?COPY required, see gjallar-recover snapshots ROOT}"
          recovery_only "$root"
          [[ $name =~ $replaced_name ]] || {
            printf '%s\n' "ERROR: $name is not a copy kept by a rollback; see gjallar-recover snapshots ROOT." >&2
            exit 1
          }
          open_top "$root"
          copy="$top/@snapshots/replaced/$name"
          btrfs subvolume show "$copy" >/dev/null 2>&1 || {
            printf '%s\n' "ERROR: there is no kept copy $name." >&2
            exit 1
          }
          if subvolume_mounted "$copy"; then
            printf '%s\n' "ERROR: $name is still in use until the next start." >&2
            exit 1
          fi
          printf '%s\n' "This deletes $name for good."
          confirm_phrase DELETE
          btrfs subvolume delete "$copy" >/dev/null
          printf '%s\n' "PASS: $name is deleted."
          ;;
        repair-boot)
          require_root
          root="''${2:?installed ROOT mountpoint required}"
          [ -e "$root/etc/NIXOS" ] || {
            printf '%s\n' 'ERROR: target is not a mounted NixOS installation.' >&2
            exit 1
          }
          mount_esp "$root"
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
          mount_esp "$root"
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

  # The trusted recovery entry of an installed system logs in with the
  # user's zsh; the recovery image itself runs bash and skips this.
  recoveryCompletion = pkgs.writeTextDir "share/zsh/site-functions/_gjallar-recover" ''
    #compdef gjallar-recover
    local -a cmds=(
      'audit:show Secure Boot, TPM and disk state'
      'unlock:open an encrypted device'
      'mount:mount a device'
      'open-root:unlock the encrypted root and mount it on /mnt'
      'generations:list the system generations of a mounted root'
      'rollback:make an older generation the next boot'
      'snapshots:list the snapshots of a mounted root'
      'snapshot-rollback:put /home or the system settings back to a snapshot'
      'snapshot-drop:delete a copy kept by a snapshot rollback'
      'repair-boot:write the boot entries of a mounted root again'
      'rebuild:rebuild a mounted root from FLAKE#HOST'
      'developer-off:turn developer mode off and go back to the official system'
      'jods:JODS repair, reinstall or fresh install'
      'install:run the installer after the recovery console erased the disk'
    )
    if (( CURRENT == 2 )); then
      _describe -t commands 'gjallar-recover command' cmds
      return
    fi
    case $words[2] in
      unlock|open-root)
        (( CURRENT == 3 )) && _files -W /dev
        ;;
      mount)
        if (( CURRENT == 3 )); then
          _files -W /dev
        elif (( CURRENT == 4 )); then
          _directories
        fi
        ;;
      generations|rollback|repair-boot|rebuild|snapshots|snapshot-rollback|snapshot-drop)
        (( CURRENT == 3 )) && _directories
        ;;
      jods)
        (( CURRENT == 3 )) && compadd repair reinstall fresh
        ;;
    esac
  '';
in
{
  inherit
    gjallarctl
    installer
    recovery
    recoveryCompletion
    recoveryConsole
    recoveryExecutor
    ;
}
