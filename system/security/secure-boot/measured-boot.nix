{
  config,
  lib,
  pkgs,
  gjallarctlPackage,
  settings,
  gjallarSecureBootArtifactVerifier,
  gjallarSecureBootOwnershipVerifier,
  ...
}:
let
  luksMappings = builtins.attrValues config.boot.initrd.luks.devices;
  luksDevice = if builtins.length luksMappings == 1 then (builtins.head luksMappings).device else "";
  tpm2Enabled = settings.secureBootEnable && settings.luksTpm2Enable;
  # Chosen at install; the first-boot enrollment asks for the PIN.
  pinEnabled = tpm2Enabled && (settings.luksTpm2Pin or false);

  enrollmentPath = [
    pkgs.cryptsetup
    gjallarctlPackage
    pkgs.systemd
    pkgs.coreutils
    # findmnt and lsblk for gjallarctl luks setup.
    pkgs.util-linux
    pkgs.gnugrep
    pkgs.kbd
    pkgs.plymouth
    gjallarSecureBootArtifactVerifier
    gjallarSecureBootOwnershipVerifier
  ];

  checkPolicy = ''
    gjallarctl installer tpm2-check-pcrlock-policy /var/lib/systemd/pcrlock.json \
      ${lib.concatMapStringsSep " " toString config.boot.lanzaboote.measuredBoot.pcrs}
  '';

  # Verifies the new token and records the keyslots; expects $device,
  # $state and $keyfile.
  recordEnrollment = ''
    mkdir -p "$state/luks"
    cryptsetup luksDump --dump-json-metadata "$device" >"$state/luks/metadata.json.tmp"
    token_id="$(
      gjallarctl installer tpm2-metadata-token-id \
        "$state/luks/metadata.json.tmp"
    )"
    # systemd ships the TPM2 token plugin, and NixOS cryptsetup dlopens
    # it by bare name, ignoring its external tokens path (e2e-target,
    # 2026-09-29: "No usable token is available" after enrollment).
    # With a startup PIN, cryptsetup reads it from stdin.
    if [ -n "''${newpin:-}" ]; then
      printf '%s' "$newpin" | LD_LIBRARY_PATH=${config.systemd.package}/lib/cryptsetup \
        cryptsetup open --test-passphrase --token-only --token-id "$token_id" "$device"
    else
      LD_LIBRARY_PATH=${config.systemd.package}/lib/cryptsetup \
        cryptsetup open --test-passphrase --token-only --token-id "$token_id" "$device"
    fi
    gjallarctl installer tpm2-write-keyslot-record \
      "$state/luks/metadata.json.tmp" \
      "$state/luks/keyslots.json" \
      "$device" \
      /var/lib/systemd/pcrlock.json
    rm -f -- "$state/luks/metadata.json.tmp"
    chmod 0600 "$state/luks/keyslots.json"
  '';

  # Rebinds TPM2 unlock after the measured state changed on purpose
  # (firmware update, Secure Boot database change). make-policy cannot
  # rewrite a PCRLock NV index whose old policy no longer matches the TPM,
  # and the first-boot enrollment refuses existing tokens, so neither path
  # recovered such a machine (e2e-target, 2026-10-05: PCR 4 PolicyOR
  # mismatch).
  tpm2Reenroll = pkgs.writeShellApplication {
    name = "gjallar-tpm2-reenroll";
    runtimeInputs = enrollmentPath;
    text = ''
      device=${lib.escapeShellArg luksDevice}
      state=/var/lib/gjallarOS

      if [ "$(id -u)" -ne 0 ]; then
        printf '%s\n' 'ERROR: run as root: sudo gjallar-tpm2-reenroll' >&2
        exit 1
      fi
      if [ ! -e "$state/tpm2-enrollment-complete" ]; then
        printf '%s\n' 'ERROR: no completed TPM2 enrollment to replace; the first enrollment runs at boot.' >&2
        exit 1
      fi

      keyfile="$(mktemp /run/gjallar-luks-key.XXXXXX)"
      trap 'rm -f -- "$keyfile" "$keyfile.meta"' EXIT

      gjallar-verify-secure-boot-artifacts
      gjallar-verify-secure-boot-ownership enrolled

      systemd-ask-password --timeout=0 -n \
        "GjallarOS: enter the disk recovery key or passphrase to re-enroll TPM2 unlock" >"$keyfile"
      chmod 0600 "$keyfile"
      # Check the key before anything is removed.
      cryptsetup open --test-passphrase --type luks "$device" --key-file "$keyfile"

      # Keep a startup PIN the old token had; a new PIN is asked for.
      cryptsetup luksDump --dump-json-metadata "$device" >"$keyfile.meta"
      pin_arg=--tpm2-with-pin=no
      newpin=
      if [ "$(gjallarctl installer tpm2-metadata-pin "$keyfile.meta")" = yes ]; then
        newpin="$(gjallarctl luks new-pin)"
        pin_arg=--tpm2-with-pin=yes
      fi
      rm -f -- "$keyfile.meta"

      if [ -e /var/lib/systemd/pcrlock.json ]; then
        ${config.systemd.package}/lib/systemd/systemd-pcrlock remove-policy
      fi
      systemctl restart systemd-pcrlock-make-policy.service
      ${checkPolicy}
      NEWPIN="$newpin" systemd-cryptenroll --wipe-slot=tpm2 --unlock-key-file="$keyfile" \
        --tpm2-device=auto --tpm2-pcrlock=/var/lib/systemd/pcrlock.json "$pin_arg" "$device"
      ${recordEnrollment}
      printf '%s\n' 'GjallarOS TPM2 unlock re-enrolled; human recovery keyslots were preserved.'
    '';
  };
in
{
  # Without tpm2-device=auto, systemd-cryptsetup takes its generic token
  # path, and a failed TPM unlock asks for a "LUKS2 token PIN" although the
  # human passphrase is what it accepts (e2e-target, 2026-10-05). That path
  # also asks for a startup PIN forever and never falls back to the recovery
  # key (e2e-fw13, 2026-10-09). systemd skips it when it measures the volume
  # key into PCR 15, which also tells later boot stages which disk was
  # opened. PCR 15 is not in the pcrlock policy. Extending the submodule sets
  # both on every device without reading the attrset back.
  options.boot.initrd.luks.devices = lib.mkOption {
    type = lib.types.attrsOf (
      lib.types.submodule {
        config.crypttabExtraOpts = lib.mkIf tpm2Enabled [
          "tpm2-device=auto"
          "tpm2-measure-pcr=yes"
        ];
      }
    );
  };

  config.environment.systemPackages = lib.mkIf tpm2Enabled [ tpm2Reenroll ];

  # make-policy rewrites the credential on the ESP here too (boot, enrollment,
  # re-enroll); its own fsyncs do not make that durable on vfat, see
  # lanzaboote.nix.
  config.systemd.services.systemd-pcrlock-make-policy = lib.mkIf tpm2Enabled {
    serviceConfig.ExecStartPost = [
      "${pkgs.coreutils}/bin/sync --file-system ${lib.escapeShellArg config.boot.loader.efi.efiSysMountPoint}"
    ];
  };

  config.assertions = lib.optionals settings.luksTpm2Enable [
    {
      assertion = settings.secureBootEnable;
      message = "GjallarOS TPM2 unlock requires Secure Boot.";
    }
    {
      assertion = builtins.length luksMappings == 1 && lib.hasPrefix "/dev/disk/by-uuid/" luksDevice;
      message = "GjallarOS TPM2 enrollment requires exactly one LUKS device addressed by /dev/disk/by-uuid/.";
    }
  ];

  config.systemd.services.measured-boot-tpm2-enrollment = lib.mkIf tpm2Enabled {
    description = "Enroll measured-boot TPM2 LUKS token";
    wantedBy = [ "multi-user.target" ];
    after = [
      "secure-boot-verification.service"
      "systemd-pcrlock-make-policy.service"
    ];
    # The passphrase prompt owns tty1 until enrollment ends; started
    # alongside greetd, the prompt vanished behind the greeter and
    # enrollment waited forever (e2e-target, 2026-09-29).
    before = [
      "display-manager.service"
      "greetd.service"
    ];
    # Not requires: the policy made before Secure Boot was enabled makes
    # this unit fail until the enrollment below replaces it.
    wants = [ "systemd-pcrlock-make-policy.service" ];
    unitConfig = {
      ConditionSecurity = "uefi-secureboot";
      ConditionPathExists = [
        "!/var/lib/gjallarOS/tpm2-enrollment-complete"
        "/var/lib/gjallarOS/secure-boot/ownership.json"
      ];
    };
    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      # Ask on tty1 directly. Plymouth's password agent got stopped while
      # the question was pending, so the typed answer never arrived
      # (e2e-target, 2026-09-29, TPM-measured boot).
      StandardInput = "tty";
      StandardOutput = "journal";
      StandardError = "journal";
      TTYPath = "/dev/tty1";
      TTYReset = true;
      TTYVHangup = true;
    };
    path = enrollmentPath;
    script = ''
      set -euo pipefail
      device=${lib.escapeShellArg luksDevice}
      state=/var/lib/gjallarOS
      keyfile="$(mktemp /run/gjallar-luks-key.XXXXXX)"
      trap 'rm -f -- "$keyfile"' EXIT

      [ ! -e "$state/secure-boot-enable-required" ] || exit 0
      gjallar-verify-secure-boot-artifacts
      gjallar-verify-secure-boot-ownership enrolled

      if cryptsetup luksDump --dump-json-metadata "$device" | grep -q 'systemd-tpm2'; then
        printf '%s\n' 'ERROR: an existing TPM2 token must be replaced with: sudo gjallarctl tpm2 reenroll; no slot was changed.' >&2
        exit 1
      fi
      # The policy from the first boot locks the PCR 7 of the time before
      # Secure Boot was enabled, and make-policy cannot rewrite its NV index
      # (e2e-fw13, 2026-10-07: AuthorizeNV policy mismatch). No token uses
      # it yet, so make it afresh.
      if [ -e /var/lib/systemd/pcrlock.json ]; then
        ${config.systemd.package}/lib/systemd/systemd-pcrlock remove-policy
      fi
      systemctl restart systemd-pcrlock-make-policy.service
      ${checkPolicy}

      plymouth quit || true
      chvt 1 || true
      systemd-ask-password --timeout=0 -n \
        "GjallarOS: enter the disk passphrase or recovery key to set up security chip unlock" >"$keyfile"
      chmod 0600 "$keyfile"
      cryptsetup open --test-passphrase --type luks "$device" --key-file "$keyfile"

      newpin=
      pin_arg=--tpm2-with-pin=no
      ${lib.optionalString pinEnabled ''
        newpin="$(gjallarctl luks new-pin)"
        pin_arg=--tpm2-with-pin=yes
      ''}
      NEWPIN="$newpin" systemd-cryptenroll --unlock-key-file="$keyfile" --tpm2-device=auto \
        --tpm2-pcrlock=/var/lib/systemd/pcrlock.json "$pin_arg" "$device"

      ${recordEnrollment}
      install -m 0600 /dev/null "$state/tpm2-enrollment-complete"
      printf '%s\n' 'GjallarOS measured-boot TPM2 enrollment completed; human recovery keyslots were preserved.'

      # A confirmed recovery key replaces the install passphrase. Cancelled,
      # the passphrase stays and `gjallarctl luks setup` finishes it later.
      gjallarctl luks setup ||
        printf '%s\n' 'Recovery key not set up; the install passphrase stays.' >&2
    '';
  };

}
