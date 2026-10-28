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
  # The firmware flash read entry (flash-read.nix) has no lockdown and opens
  # the disk with the recovery key. Its UKI never gets a PCR 4 variant, so
  # the TPM does not unseal in that boot. lzbt then rebuilds that UKI at
  # every install, as it does for any UKI without a pcrlock file.
  unmeasuredEntries = [ "gjallar-flash-read" ];
  measuredSpecialisations = lib.subtractLists unmeasuredEntries (
    builtins.attrNames config.specialisation
  );
  ukisPerGeneration = 1 + builtins.length measuredSpecialisations;
  esp = lib.escapeShellArg config.boot.loader.efi.efiSysMountPoint;
  cfg = config.boot.lanzaboote;
  systemdLib = "${config.systemd.package}/lib/systemd";
  jq = "${pkgs.jq}/bin/jq";

  # lzbt keeps the pcrlock variants of installed boot files only. With one
  # generation (recovery) the UKI that is running loses its variant at every
  # rebuild, and a new systemd-boot replaces the old one's. make-policy then
  # finds this boot's PCR 4 log unrecognized and drops PCR 4 until the next
  # boot (e2e-fw13, 2026-10-12): that boot unlocked with PCR 0 and 7 only, so
  # any older UKI signed with the same key would have unlocked the disk, and
  # tpm2 reenroll or a dbx update refused to bind. The hook keeps what this
  # boot measured as one more variant; boot removes it before make-policy.
  keepBooted = cfg.measuredBoot.enable && lib.elem 4 cfg.measuredBoot.pcrs;
  bootedVariant = "gjallar-booted.pcrlock";
  bootedComponents = [
    "630-bootloader"
    "635-lanzaboote"
  ];
  componentDir = component: "${cfg.measuredBoot.pcrlockDirectory}/${component}.pcrlock.d";

  # Writes $booted/<component>, a pcrlock variant built from this boot's
  # event log records of that component.
  saveBooted = ''
    systemd-pcrlock log --json=short |
      ${jq} -r --argjson components ${lib.escapeShellArg (builtins.toJSON bootedComponents)} '
        [.log[] | select(.pcr == 4 and (.component | IN($components[])))]
        | group_by(.component)[]
        | [
            .[0].component,
            ({records: map({pcr, digests: [to_entries[] | select(.key | test("^sha[0-9]+$")) | {hashAlg: .key, digest: .value}]})} | tojson)
          ]
        | @tsv' |
      while IFS=$'\t' read -r component variant; do
        printf '%s\n' "$variant" >"$booted/$component"
      done
  '';

  # Adds each saved variant that lzbt no longer has. The variants of all
  # PCR 4 components multiply; past the pcrlock limit nothing is added and
  # PCR 4 stays unbound until the restart, as without this.
  restoreBooted = ''
    added=()
    for variant in "$booted"/*; do
      dir=${lib.escapeShellArg cfg.measuredBoot.pcrlockDirectory}/"''${variant##*/}.pcrlock.d"
      rm -f -- "$dir/${bootedVariant}"
      if ${jq} -n -e --slurpfile kept "$variant" '
          [inputs | .records[].digests[] | select(.hashAlg == "sha256").digest] as $known
          | all($kept[0].records[].digests[] | select(.hashAlg == "sha256").digest; IN($known[]))' \
          "$dir"/*.pcrlock </dev/null >/dev/null; then
        continue
      fi
      install -m 0644 -- "$variant" "$dir/${bootedVariant}"
      added+=("$dir/${bootedVariant}")
    done
    alternatives=1
    ${lib.concatMapStrings (component: ''
      variants=(${lib.escapeShellArg (componentDir component)}/*.pcrlock)
      alternatives=$((alternatives * (''${#variants[@]} > 1 ? ''${#variants[@]} : 1)))
    '') bootedComponents}
    if [ "$alternatives" -gt ${toString pcrlockAlternatives} ]; then
      rm -f -- "''${added[@]}"
      echo "warning: too many boot entries to also allow the running boot; PCR 4 is not bound until the next restart." >&2
    fi
  '';

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
    shopt -s nullglob
    PATH=${systemdLib}:${pkgs.coreutils}/bin:$PATH
    # make-policy cannot update the TPM from that boot: it unseals with the
    # old prediction, which this boot's PCR 4 never matches.
    if [[ " $(</proc/cmdline) " == *" gjallar.flash-read "* ]]; then
      echo "error: this is the firmware flash read entry. Restart into the normal entry to rebuild." >&2
      exit 1
    fi
    # The same holds for the recovery partition, whose UKI is in PCR 4 when
    # gjallar-recover rebuilds through nixos-enter, which mounts this /proc
    # (e2e-fw13, 2026-10-28). make-policy failed there and so did the rebuild.
    recovery=
    if [[ " $(</proc/cmdline) " == *" gjallar.recovery=partition "* ]]; then
      recovery=1
    fi
    has_tpm2=
    ${lib.optionalString cfg.measuredBoot.enable ''
      if [ -z "$recovery" ] && ${config.systemd.package}/bin/systemd-analyze has-tpm2 >/dev/null 2>&1; then
        has_tpm2=1
      fi
    ''}
    ${lib.optionalString keepBooted ''
      booted="$(mktemp -d)"
      trap 'rm -rf -- "$booted"' EXIT
      if [ -n "$has_tpm2" ]; then
        ${saveBooted}
      fi
    ''}
    ${lib.concatMapStringsSep "\n" installOnEsp (
      [ config.boot.loader.efi.efiSysMountPoint ] ++ cfg.extraEfiSysMountPoints
    )}
    ${lib.optionalString cfg.measuredBoot.enable ''
      ${lib.optionalString (lib.elem 4 cfg.measuredBoot.pcrs) (
        lib.concatMapStrings (entry: ''
          rm -f -- ${lib.escapeShellArg (componentDir "635-lanzaboote")}/*-${entry}.pcrlock
        '') unmeasuredEntries
      )}
      if [ -n "$has_tpm2" ]; then
        ${lib.optionalString keepBooted restoreBooted}
        echo "Predicting the PCR state for future boots..."
        ${lib.last config.systemd.services.systemd-pcrlock-make-policy.serviceConfig.ExecStart}
      elif [ -n "$recovery" ]; then
        # The old policy still covers the boot files it knew, so the same
        # system unlocks as before (e2e-fw13, 2026-10-28).
        echo "warning: the recovery partition cannot update the TPM policy; if the next boot starts a system the TPM policy does not know, it asks for the recovery key or disk passphrase." >&2
        echo "warning: after such a boot, run: gjallarctl tpm2 reenroll" >&2
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

  # "!": only systemd-tmpfiles-setup at boot removes them; its rerun on a
  # switch has no --boot.
  systemd.tmpfiles.rules = lib.mkIf keepBooted (
    map (component: "r! ${componentDir component}/${bootedVariant}") bootedComponents
  );
  systemd.services.systemd-pcrlock-make-policy.after = lib.mkIf keepBooted [
    "systemd-tmpfiles-setup.service"
  ];

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
