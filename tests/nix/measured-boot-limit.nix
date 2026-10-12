{
  nixpkgs,
  system,
  lanzabooteModule,
  secureBootModule,
  flashReadModule,
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
        {
          system.stateVersion = "26.05";
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
in
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
assert nixpkgs.lib.elem "tpm2-device=auto" flashRead.boot.initrd.luks.devices.cryptroot.crypttabExtraOpts;
# The wrapped hook also drops systemd-boot's stale entries.
assert nixpkgs.lib.hasInfix "install-lanzaboote" "${recovery.system.build.installBootLoader}";
nixpkgs.legacyPackages.${system}.runCommand "gjallar-measured-boot-limit-check" { } ''
  touch "$out"
''
