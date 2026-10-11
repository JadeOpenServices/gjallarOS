{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  # systemd-pcrlock supports at most 8 alternatives per PCR, and PCR 4
  # measures every installed UKI: one per generation plus one per
  # specialisation (trusted recovery, recovery-storage maintenance). With
  # more, every bootloader install fails ("Failed to calculate super PCR
  # policy: Argument list too long"), e2e-full, 2026-09-29.
  pcrlockAlternatives = 8;
  ukisPerGeneration = 1 + builtins.length (builtins.attrNames config.specialisation);
  esp = lib.escapeShellArg config.boot.loader.efi.efiSysMountPoint;
  cfg = config.boot.lanzaboote;
  systemdLib = "${config.systemd.package}/lib/systemd";

  # The same lzbt call lanzaboote's own hook makes, per ESP.
  installOnEsp =
    mountPoint:
    "${cfg.installCommand} "
    + lib.escapeShellArgs (
      [
        "--public-key=${toString cfg.publicKeyFile}"
        "--private-key=${toString cfg.privateKeyFile}"
      ]
      ++ lib.optional (lib.elem 4 cfg.measuredBoot.pcrs) "--pcrlock-directory=${cfg.measuredBoot.pcrlockDirectory}"
      ++ [ mountPoint ]
    )
    + " /nix/var/nix/profiles/system-*-link";

  # lanzaboote's hook runs make-policy unconditionally and returns only its
  # status. With the TPM off in firmware, make-policy cannot reach
  # /dev/tpmrm0 and every rebuild failed at "Failed to install bootloader"
  # (e2e-fw13, 2026-10-10); a failing lzbt was hidden whenever make-policy
  # then worked. systemd skips its own make-policy unit the same way
  # (ConditionSecurity=measured-uki). The old policy stays in the TPM and
  # cannot heal itself: make-policy unseals its recovery PIN under the old
  # prediction, which the new UKI no longer matches (systemd v260
  # pcrlock.c make_policy; e2e-fw13, 2026-10-10). Every boot with the TPM
  # back asks for the passphrase until `gjallarctl tpm2 reenroll`.
  installHook = pkgs.writeShellScript "lzbt-install" ''
    set -euo pipefail
    PATH=${systemdLib}:$PATH
    ${lib.concatMapStringsSep "\n" installOnEsp (
      [ config.boot.loader.efi.efiSysMountPoint ] ++ cfg.extraEfiSysMountPoints
    )}
    ${lib.optionalString cfg.measuredBoot.enable ''
      if ${config.systemd.package}/bin/systemd-analyze has-tpm2 >/dev/null 2>&1; then
        echo "Predicting the PCR state for future boots..."
        ${lib.last config.systemd.services.systemd-pcrlock-make-policy.serviceConfig.ExecStart}
      else
        echo "warning: no usable TPM2 (see: systemd-analyze has-tpm2); the TPM policy was not updated." >&2
        echo "warning: once the TPM is back, boot with the recovery key or disk passphrase and run: gjallarctl tpm2 reenroll" >&2
      fi
    ''}
  '';
in
{
  assertions = lib.optional settings.luksTpm2Enable {
    assertion = ukisPerGeneration <= pcrlockAlternatives;
    message = "Measured boot supports at most ${toString pcrlockAlternatives} UKIs per generation; ${toString ukisPerGeneration} are configured.";
  };

  boot.loader.grub.enable = lib.mkIf settings.secureBootEnable (lib.mkForce false);
  boot.loader.systemd-boot.enable = lib.mkIf settings.secureBootEnable (lib.mkForce false);

  boot.lanzaboote = lib.mkIf settings.secureBootEnable {
    enable = true;
    pkiBundle = "/var/lib/sbctl";
    # With recovery, older generations are chosen there (gjallar-recover
    # rollback), not in the boot menu.
    configurationLimit =
      if settings.recoveryEnable or false then
        1
      else if settings.luksTpm2Enable then
        lib.max 1 (pcrlockAlternatives / ukisPerGeneration)
      else
        8;

    measuredBoot = lib.mkIf settings.luksTpm2Enable {
      enable = true;

      pcrs = [
        0
        4
        7
      ];
    };
  };

  # systemd-boot's entries outlive a switch to Secure Boot, and lanzaboote
  # already removed the kernels they load.
  #
  # lzbt syncs the ESP before its garbage collection and before make-policy
  # writes the pcrlock credential. On vfat a rename over an existing file
  # reaches the disk through the file's inode, which make-policy's directory
  # fsync does not write. A power loss within ~30 s left the old directory
  # entry pointing at freed clusters; fsck cut the credential to 0 bytes and
  # TPM unlock failed with "Encrypted file too short" (e2e-fw13, 2026-10-10,
  # after rollback). Sync last, as the systemd-boot builder does.
  system.build.installBootLoader = lib.mkIf settings.secureBootEnable (
    lib.mkForce (
      pkgs.writeShellScript "install-lanzaboote" ''
        set -euo pipefail
        ${installHook}
        ${pkgs.coreutils}/bin/rm -f ${esp}/loader/entries/nixos*-generation-*.conf
        ${pkgs.coreutils}/bin/sync --file-system ${esp}
      ''
    )
  );
}
