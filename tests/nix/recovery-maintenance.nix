{
  nixpkgs,
  system,
  recoveryModule,
  # Trees scanned for systemd-ask-password calls feeding cryptsetup.
  sources,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};
  evaluate =
    extra:
    nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        recoveryModule
        {
          system.stateVersion = "26.05";
          fileSystems."/" = {
            device = "/dev/mapper/cryptroot";
            fsType = "btrfs";
          };
        }
        extra
      ];
      specialArgs = {
        sourceRevision = "test";
        settings = {
          recoveryEnable = true;
          prebootLock.enable = false;
          management.enable = false;
          secureBootEnable = false;
          luksTpm2Enable = false;
        };
      };
    };
  evaluated = evaluate {
    boot.initrd.luks.devices.cryptroot.device = "/dev/disk/by-partlabel/root";
  };
  splash = evaluate {
    boot.initrd.luks.devices.cryptroot.device = "/dev/disk/by-partlabel/root";
    boot.plymouth.enable = true;
  };
  splashMaintenance = splash.config.specialisation.gjallar-recovery-maintenance.configuration;
  plain = evaluate {
    fileSystems."/".device = nixpkgs.lib.mkForce "/dev/disk/by-partlabel/root";
  };
  # specialisation.<name>.configuration is the evaluated child config.
  maintenance = evaluated.config.specialisation.gjallar-recovery-maintenance.configuration;
  unit = maintenance.boot.initrd.systemd.services.recovery-storage-setup;
  script = unit.script;
in
# The maintenance initrd found no root when the credential kept the newline
# systemd-ask-password prints by default: cryptsetup --key-file uses every
# byte of the file (e2e-full, 2026-09-29).
assert pkgs.lib.hasInfix "systemd-ask-password --timeout=0 -n " script;
# systemd-cryptsetup@cryptroot took the typed credential first, so the
# script's cryptsetup open hit EBUSY and dropped to emergency mode.
assert builtins.elem "systemd-cryptsetup@cryptroot.service" unit.before;
assert pkgs.lib.hasInfix "try_candidate cryptroot /dev/disk/by-partlabel/root" script;
# A mistyped credential stopped at the initrd emergency shell, which the
# locked root account cannot open; now three tries, then a normal reboot.
assert pkgs.lib.hasInfix "for attempt in 1 2 3; do" script;
assert pkgs.lib.hasInfix "systemctl reboot --force --force" script;
assert pkgs.lib.hasInfix "Rebooting to the normal gjallarOS boot path" script;
# Plymouth hides console text; the retry and reboot notices must reach it.
assert pkgs.lib.hasInfix "did not unlock storage (try" script;
assert pkgs.lib.hasInfix "plymouth display-message" script;
# The unit PATH is only its path list; the initrd /bin plymouth is not on it.
assert builtins.elem splashMaintenance.boot.plymouth.package
  splashMaintenance.boot.initrd.systemd.services.recovery-storage-setup.path;
# Without LUKS there is nothing to unlock, so no maintenance entry at all.
assert !(plain.config.specialisation ? gjallar-recovery-maintenance);
pkgs.runCommand "gjallar-recovery-maintenance-check"
  {
    nativeBuildInputs = [
      pkgs.cryptsetup
      pkgs.python3
    ];
  }
  ''
    # The contract the fix relies on, checked against real cryptsetup.
    truncate -s 32M disk.img
    printf 'secret' > key
    printf 'secret\n' > key-newline
    cryptsetup luksFormat --batch-mode --disable-locks --type luks2 \
      --pbkdf pbkdf2 --pbkdf-force-iterations 1000 --key-file key disk.img
    cryptsetup open --disable-locks --test-passphrase --key-file key disk.img
    if cryptsetup open --disable-locks --test-passphrase --key-file key-newline disk.img; then
      echo "cryptsetup accepted a key file with a trailing newline" >&2
      exit 1
    fi

    # The flags must exist in the pinned systemd: the first fix used
    # --newline=no, which systemd 260 rejects ("unrecognized option"), so the
    # maintenance initrd still dropped to emergency mode.
    ${pkgs.systemd}/bin/systemd-ask-password --timeout=0 -n --help > /dev/null

    # Same bug in every prompt that feeds cryptsetup or systemd-cryptenroll:
    # each systemd-ask-password call must pass -n (no trailing newline).
    # Unit names such as systemd-ask-password-plymouth.path are not calls.
    sources=(${pkgs.lib.escapeShellArgs (map (source: "${source}") sources)})
    for source in "''${sources[@]}"; do
      test -d "$source"
    done
    if grep -rnE 'systemd-ask-password([^-]|$)' "''${sources[@]}" |
      grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' |
      grep -vE -- 'systemd-ask-password( --timeout=0)? -n '
    then
      echo "systemd-ask-password without -n (see above)" >&2
      exit 1
    fi

    # A rerun after a failed maintenance boot has one maintenance entry per
    # generation; only the freshly activated generation may be armed.
    select=${recoveryModule}/select-maintenance-entry.py
    cat > entries.json <<'JSON'
    [
      {"id": "nixos-generation-4-mjlrvem2wdmub2opvmjfggilkugfihbgiernzkh5cyqmdop4z5ya.efi"},
      {"id": "nixos-generation-4-specialisation-gjallar-recovery-p4b4izsszjakqim5npcb5qcfbopz5daowbbaxsbfsqgvizr3iivq.efi"},
      {"id": "nixos-generation-4-specialisation-gjallar-recovery-maintenance-p4b4izsszjakqim5npcb5qcfbopz5daowbbaxsbfsqgvizr3iivq.efi"},
      {"id": "nixos-generation-3-specialisation-gjallar-recovery-maintenance-6uusfxyovl7h7b643a3yp2ejkatoieicxemiw4wypcoua2ehcdlq.efi"},
      {"id": "nixos-generation-40-specialisation-gjallar-recovery-maintenance.conf"},
      {"id": "nixos-generation-2.conf"},
      {"id": "auto-reboot-to-firmware-setup"}
    ]
    JSON
    test "$(python3 "$select" 4 < entries.json)" = \
      nixos-generation-4-specialisation-gjallar-recovery-maintenance-p4b4izsszjakqim5npcb5qcfbopz5daowbbaxsbfsqgvizr3iivq.efi
    test "$(python3 "$select" 40 < entries.json)" = \
      nixos-generation-40-specialisation-gjallar-recovery-maintenance.conf
    for bad in 2 5 x ""; do
      if python3 "$select" "$bad" < entries.json; then
        echo "selected a maintenance entry for generation '$bad'" >&2
        exit 1
      fi
    done
    touch "$out"
  ''
