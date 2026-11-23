{
  nixpkgs,
  system,
  lanzabooteModule,
  secureBootModule,
  flashReadModule,
  tpm2ClearModule,
}:
let
  evaluate =
    luksTpm2Enable: recoveryEnable:
    evaluateWith {
      inherit luksTpm2Enable recoveryEnable;
    };
  evaluateWith =
    extraSettings:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        lanzabooteModule
        secureBootModule
        flashReadModule
        # lanzaboote.nix calls the chip-slot cleanup this module provides.
        tpm2ClearModule
        {
          system.stateVersion = "26.05";
          # As on every host (system/hardware/firmware.nix).
          services.fwupd.enable = true;
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "ext4";
          };
          # As older installers wrote it into generated/hardware.nix.
          boot.initrd.luks.devices.cryptroot = {
            device = "/dev/null";
            crypttabExtraOpts = [
              "tries=0"
              "tpm2-device=auto"
            ];
          };
          # Same shape as system/recovery: trusted recovery + maintenance.
          specialisation.gjallar-recovery.configuration = { };
          specialisation.gjallar-recovery-maintenance.configuration = { };
        }
      ];
      specialArgs.gjallarctlPackage = nixpkgs.legacyPackages.${system}.emptyDirectory;
      specialArgs.settings = {
        secureBootEnable = true;
      }
      // extraSettings;
    }).config;
  measured = (evaluate true false).boot.lanzaboote;
  unmeasured = (evaluate false false).boot.lanzaboote;
  recovery = evaluate true true;
  flashRead = evaluateWith {
    luksTpm2Enable = true;
    recoveryEnable = false;
    firmwareFlashReadEntry = true;
  };
  flashReadEntry = flashRead.specialisation.gjallar-flash-read.configuration;
  shim = evaluateWith {
    luksTpm2Enable = true;
    recoveryEnable = false;
    secureBootShim = true;
  };
  noShim = evaluate true false;
  lib = nixpkgs.lib;
  # The hook text split at the firmware entry step. On a rebuild it runs
  # before make-policy; on a fresh install only after it worked (friend's
  # T480s, .68, 2026-11-22: make-policy refused after the entry already
  # started shim from the new disk, MokManager loop).
  hookParts =
    config:
    lib.filter builtins.isString (
      builtins.split "gjallar-boot-first" (
        builtins.unsafeDiscardStringContext config.system.build.gjallarLzbtInstallHook.text
      )
    );
  lastIf = text: lib.last (lib.splitString "if [" text);
  hookOrderOk =
    config:
    let
      parts = hookParts config;
      before = builtins.elemAt parts 0;
      between = builtins.elemAt parts 1;
    in
    builtins.length parts == 3
    && lib.hasInfix "if [ ! -e /run/booted-system ]; then" before
    && lib.hasPrefix " -z \"$fresh\" ]" (lastIf before)
    && lib.hasInfix "Predicting the PCR state" between
    && lib.hasInfix "exit 1" between
    && lib.hasPrefix " -n \"$fresh\" ]" (lastIf between);
in
assert hookOrderOk shim;
assert hookOrderOk noShim;
# systemd-pcrlock: at most 8 alternatives per PCR; PCR 4 sees every UKI.
assert measured.measuredBoot.enable;
assert measured.configurationLimit * 3 <= 8;
assert measured.configurationLimit == 2;
assert unmeasured.configurationLimit == 8;
# With recovery, older generations are picked there, not in the boot menu.
assert recovery.boot.lanzaboote.configurationLimit == 1;
# The flash read entry is off by default and gets no PCR 4 variant, so it
# leaves the boot menu limit alone.
assert !((evaluate true false).specialisation ? gjallar-flash-read);
assert flashRead.boot.lanzaboote.configurationLimit == 2;
assert nixpkgs.lib.elem "iomem=relaxed" flashReadEntry.boot.kernelParams;
assert !(nixpkgs.lib.elem "iomem=relaxed" flashRead.boot.kernelParams);
# A failed unseal with a startup PIN asked for the PIN forever there.
assert flashReadEntry.boot.initrd.luks.devices.cryptroot.crypttabExtraOpts == [ "tries=0" ];
assert nixpkgs.lib.elem "tpm2-device=auto"
  flashRead.boot.initrd.luks.devices.cryptroot.crypttabExtraOpts;
# Shim mode (Plane 293): fwupd starts its capsule loader through shim, and
# the install hook differs from the one without shim (--shim-directory).
assert shim.services.fwupd.uefiCapsuleSettings.DisableShimForSecureBoot == false;
assert noShim.services.fwupd.uefiCapsuleSettings.DisableShimForSecureBoot == true;
assert shim.system.build.installBootLoader.drvPath != noShim.system.build.installBootLoader.drvPath;
assert shim.boot.lanzaboote.configurationLimit == noShim.boot.lanzaboote.configurationLimit;
# The wrapped hook also drops systemd-boot's stale entries.
assert nixpkgs.lib.hasInfix "install-lanzaboote" "${recovery.system.build.installBootLoader}";
# Without measured boot there is no make-policy unit at all; a bare
# .after left an empty unit without ExecStart= (fw13, 2026-11-03).
assert !((evaluate false false).systemd.services ? systemd-pcrlock-make-policy);
assert builtins.elem "systemd-tmpfiles-setup.service"
  noShim.systemd.services.systemd-pcrlock-make-policy.after;
nixpkgs.legacyPackages.${system}.runCommand "gjallar-measured-boot-limit-check" { } ''
  touch "$out"
''
