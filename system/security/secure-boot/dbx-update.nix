{
  config,
  lib,
  pkgs,
  gjallarctlPackage,
  settings,
  gjallarSecureBootOwnershipVerifier,
  ...
}:
let
  tpm2Enabled = settings.secureBootEnable && settings.luksTpm2Enable;

  # systemd-pcrlock lock-secureboot-policy writes what this boot measured
  # into PCR 7 here; each file in the folder is one allowed variant.
  policyDir = "/var/lib/pcrlock.d/240-secureboot-policy.pcrlock.d";
  previousPolicy = "${policyDir}/gjallar-before-dbx-update.pcrlock";

  # Installs the newest dbx (revocation list) from LVFS. The update is
  # signed by Microsoft's KEK, which enrollment keeps, so it applies under
  # GjallarOS ownership. fwupd writes the variable now; firmware measures
  # it into PCR 7 from the next boot on. Until then both the current and
  # the new state stay valid, so a rebuild or `gjallarctl tpm2 reenroll`
  # before the restart still finds a matching PCR 7.
  dbxUpdate = pkgs.writeShellApplication {
    name = "gjallar-secure-boot-dbx-update";
    runtimeInputs = [
      config.services.fwupd.package
      pkgs.jq
      pkgs.coreutils
      pkgs.systemd
      gjallarctlPackage
      gjallarSecureBootOwnershipVerifier
    ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        printf '%s\n' 'ERROR: run as root: sudo gjallar-secure-boot-dbx-update' >&2
        exit 1
      fi

      gjallar-verify-secure-boot-ownership enrolled

      fwupdmgr refresh --force
      # Assigned first, so a failing fwupdmgr stops here instead of reading
      # as an empty list.
      devices="$(fwupdmgr get-devices --json)"
      device="$(
        jq -r '[.Devices[] | select(.Plugin == "uefi_dbx")][0].DeviceId // empty' <<<"$devices"
      )"
      if [ -z "$device" ]; then
        printf '%s\n' 'ERROR: fwupd found no UEFI dbx on this machine; nothing was changed.' >&2
        exit 1
      fi
      # With --json, "nothing to do" is an empty list, not exit code 2. LVFS
      # only offers dbx updates signed for a Microsoft KEK this machine has.
      updates="$(fwupdmgr get-updates "$device" --json)"
      if ! jq -e 'has("Devices")' >/dev/null <<<"$updates"; then
        printf 'ERROR: fwupd could not list dbx updates: %s\n' "$updates" >&2
        exit 1
      fi
      if [ "$(jq '.Devices | length' <<<"$updates")" -eq 0 ]; then
        printf '%s\n' 'LVFS has no newer dbx revocation list for this machine; nothing was changed.'
        exit 0
      fi

      ${lib.optionalString tpm2Enabled ''
        previous=${lib.escapeShellArg previousPolicy}
        created=
        # A second update before the restart keeps the first copy: that is
        # still what this boot measured.
        if [ ! -e "$previous" ]; then
          cp -- ${lib.escapeShellArg "${policyDir}/generated.pcrlock"} "$previous"
          created=1
        fi
        trap '[ -z "$created" ] || rm -f -- "$previous"' ERR
      ''}
      fwupdmgr update "$device" --assume-yes --no-reboot-check
      ${lib.optionalString tpm2Enabled ''
        trap - ERR
        ${config.systemd.package}/lib/systemd/systemd-pcrlock lock-secureboot-policy
        systemctl restart systemd-pcrlock-make-policy.service
        gjallarctl installer tpm2-check-pcrlock-policy /var/lib/systemd/pcrlock.json \
          ${lib.concatMapStringsSep " " toString config.boot.lanzaboote.measuredBoot.pcrs}
      ''}
      printf '%s\n' 'New dbx revocation list written; it takes effect after the next restart.'
    '';
  };
in
lib.mkIf settings.secureBootEnable {
  assertions = [
    {
      # 2.0.8 is the first fwupd that knows Microsoft's 2023 CA rotation
      # (its uefi-kek and uefi-db plugins), which dbx updates now follow.
      assertion = lib.versionAtLeast config.services.fwupd.package.version "2.0.8";
      message = "GjallarOS dbx updates need fwupd 2.0.8 or newer.";
    }
  ];

  environment.systemPackages = [ dbxUpdate ];

  # At the next boot the firmware measures the new dbx, so the state from
  # before the update no longer unlocks the disk. "!": only
  # systemd-tmpfiles-setup at boot removes it, before make-policy (see
  # lanzaboote.nix). A rebuild restarts changed units, so an ExecStartPre
  # here would have dropped it before the restart.
  systemd.tmpfiles.rules = lib.mkIf tpm2Enabled [ "r! ${previousPolicy}" ];
}
