{
  config,
  lib,
  pkgs,
  gjallarctlPackage,
  settings,
  ...
}:
let
  luksMappings = builtins.attrValues config.boot.initrd.luks.devices;
  luksDevice = if builtins.length luksMappings == 1 then (builtins.head luksMappings).device else "";

  # Removes what security chip unlock left behind once it is switched off
  # (loophole 47): the chip slot, the startup policy in the TPM and the
  # enrollment marker. A chip slot left in place kept opening the disk
  # through systemd's generic token path, and the old policy in the TPM
  # made the rebuild that switches chip unlock back on fail (e2e-fw13,
  # 2026-11-10). Chip slots without a startup policy go too (loophole 40,
  # Q222). The slot goes only while a recovery key or passphrase remains.
  # Run as root, with the LUKS device as the only argument.
  tpm2Clear = pkgs.writeShellApplication {
    name = "gjallar-tpm2-clear";
    runtimeInputs = [
      pkgs.cryptsetup
      gjallarctlPackage
      config.systemd.package
      pkgs.coreutils
      # findmnt and lsblk for gjallarctl luks record.
      pkgs.util-linux
    ];
    text = ''
      device="$1"
      state=/var/lib/gjallarOS
      meta="$(mktemp /run/gjallar-luks-meta.XXXXXX)"
      trap 'rm -f -- "$meta"' EXIT

      changed=
      cryptsetup luksDump --dump-json-metadata "$device" >"$meta"
      if [ "$(gjallarctl installer tpm2-metadata-policy "$meta")" != none ]; then
        if [ "$(gjallarctl installer tpm2-metadata-human "$meta")" != yes ]; then
          printf 'ERROR: %s opens only with the security chip; its chip slot stays until a recovery key is set up: gjallarctl luks recovery-key\n' \
            "$device" >&2
          exit 1
        fi
        systemd-cryptenroll --wipe-slot=tpm2 "$device"
        printf '%s\n' 'Security chip unlock is off; its chip slot was removed. The disk opens with the recovery key or passphrase.'
        changed=1
      fi

      if [ -e /var/lib/systemd/pcrlock.json ]; then
        if systemd-analyze has-tpm2 >/dev/null 2>&1; then
          ${config.systemd.package}/lib/systemd/systemd-pcrlock remove-policy
          changed=1
        else
          # The enrollment removes it once the TPM is back.
          printf '%s\n' 'warning: no usable TPM2 (see: systemd-analyze has-tpm2); the old startup policy stays in the TPM for now.' >&2
        fi
      fi

      if [ -e "$state/tpm2-enrollment-complete" ] || [ -e "$state/luks/keyslots.json" ]; then
        rm -f -- "$state/tpm2-enrollment-complete" "$state/luks/keyslots.json"
        changed=1
      fi

      # The bar and the login card follow the keyslots. At boot the record
      # service runs after this; on a switch or rebuild it ran already.
      if [ -n "$changed" ] && [ -d /run/gjallar-luks ]; then
        gjallarctl luks record ||
          printf '%s\n' 'warning: the disk unlock record was not updated; the next start writes it.' >&2
      fi
    '';
  };
in
{
  _module.args.gjallarTpm2Clear = tpm2Clear;

  # Chip unlock off: clear what it left at every start, before the record
  # the bar and login card read. A no-op once nothing is left.
  systemd.services.gjallar-tpm2-clear = lib.mkIf (!settings.luksTpm2Enable && luksDevice != "") {
    description = "Remove security chip unlock left from before it was switched off";
    wantedBy = [ "multi-user.target" ];
    before = [ "gjallar-luks-record.service" ];
    serviceConfig = {
      Type = "oneshot";
      UMask = "0077";
      ExecStart = "${tpm2Clear}/bin/gjallar-tpm2-clear ${lib.escapeShellArg luksDevice}";
    };
  };
}
