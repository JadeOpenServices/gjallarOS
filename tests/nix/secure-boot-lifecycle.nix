{
  nixpkgs,
  system,
  lifecycleModule,
  measuredBootModule,
  dbxUpdateModule,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};
  stub = pkgs.emptyDirectory;
  evaluated = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [
      lifecycleModule
      measuredBootModule
      dbxUpdateModule
      {
        options.boot.lanzaboote.measuredBoot.pcrs = nixpkgs.lib.mkOption {
          default = [
            0
            4
            7
          ];
        };
      }
      {
        boot.initrd.luks.devices.cryptroot.device =
          "/dev/disk/by-uuid/00000000-0000-0000-0000-000000000000";
        system.stateVersion = "26.05";
        boot.loader.grub.enable = false;
        fileSystems."/" = {
          device = "/dev/null";
          fsType = "ext4";
        };
      }
    ];
    specialArgs = {
      gjallarctlPackage = stub;
      gjallarSecureBootArtifactVerifier = stub;
      gjallarSecureBootOwnershipVerifier = stub;
      settings = {
        secureBootEnable = true;
        luksTpm2Enable = true;
      };
    };
  };
  post = evaluated.config.systemd.services.installer-post-secure-boot;
  enrollment = evaluated.config.systemd.services.secure-boot-enrollment;
  tpm2 = evaluated.config.systemd.services.measured-boot-tpm2-enrollment;
in
# The TPM2 unlock self-test needs systemd's cryptsetup token plugin, which
# NixOS cryptsetup only finds through the library path; it ignores
# --external-tokens-path (e2e-target, 2026-09-29).
assert nixpkgs.lib.hasInfix
  (builtins.unsafeDiscardStringContext "LD_LIBRARY_PATH=${evaluated.config.systemd.package}/lib/cryptsetup \\\n    cryptsetup open --test-passphrase --token-only --token-id")
  (builtins.unsafeDiscardStringContext tpm2.script);
assert !(nixpkgs.lib.hasInfix "--external-tokens-path" tpm2.script);
# pcrlock drops PCRs it cannot predict; enrollment must refuse a policy
# that no longer locks the measured-boot PCRs (e2e-target, 2026-09-29:
# "pcrValues":[] bound the disk key to nothing).
assert nixpkgs.lib.hasInfix "tpm2-check-pcrlock-policy /var/lib/systemd/pcrlock.json \\\n  0 4 7"
  tpm2.script;
# The TPM2 passphrase prompt needs Plymouth, which greetd quits (e2e-target,
# 2026-09-29: prompt hidden behind the greeter, enrollment hung).
assert builtins.elem "display-manager.service" tpm2.before;
assert builtins.elem "greetd.service" tpm2.before;
# Plymouth's password agent was stopped while the TPM2 question was pending
# and the answer was lost (e2e-target, 2026-09-29); ask on tty1 directly.
assert tpm2.serviceConfig.StandardInput == "tty";
assert tpm2.serviceConfig.TTYPath == "/dev/tty1";
assert nixpkgs.lib.hasInfix "plymouth quit || true\nchvt 1 || true\nsystemd-ask-password"
  tpm2.script;
# gjallarctl runs sbctl verify in-process after enrolling; without lsblk the
# enrollment transaction failed after the keys were written (e2e-target,
# 2026-09-29).
assert builtins.elem pkgs.util-linux enrollment.path;
# Started in parallel with enrollment, the continuation told the user to
# remove PK while firmware already was in Setup Mode (e2e-target, 2026-09-29).
assert builtins.elem "secure-boot-enrollment.service" post.after;
# Without tpm2-device=auto, systemd-cryptsetup took its generic token path
# and a failed TPM unlock asked for a "LUKS2 token PIN" although only the
# passphrase works (e2e-target, 2026-10-05).
assert
  evaluated.config.boot.initrd.luks.devices.cryptroot.crypttabExtraOpts == [
    "tpm2-device=auto"
    "tpm2-measure-pcr=yes"
  ];
# A PCRLock policy that no longer matches the TPM had no way back to TPM2
# unlock (e2e-target, 2026-10-05).
assert builtins.any (
  p: (p.name or "") == "gjallar-tpm2-reenroll"
) evaluated.config.environment.systemPackages;
# A dbx update keeps the PCR 7 of the running boot valid until the restart;
# the next boot must drop it before make-policy runs, and a rebuild before
# that restart must not (e2e-fw13, 2026-10-12).
assert builtins.any (
  p: (p.name or "") == "gjallar-secure-boot-dbx-update"
) evaluated.config.environment.systemPackages;
assert builtins.elem "r! /var/lib/pcrlock.d/240-secureboot-policy.pcrlock.d/gjallar-before-dbx-update.pcrlock" evaluated.config.systemd.tmpfiles.rules;
pkgs.runCommand "gjallar-secure-boot-lifecycle-check" { } "touch $out"
