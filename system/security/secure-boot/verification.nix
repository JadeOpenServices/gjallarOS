{
  lib,
  pkgs,
  settings,
  gjallarctlPackage,
  ...
}:
let
  # A system set to boot through shim (Plane 293) counts a shim boot as
  # verified; any other system fails when it comes up through shim.
  shim = settings.secureBootEnable && (settings.secureBootShim or false);
  secureBootArtifactVerifier = pkgs.writeShellApplication {
    name = "gjallar-verify-secure-boot-artifacts";
    runtimeInputs = [
      pkgs.sbctl
      pkgs.util-linux
      pkgs.coreutils
      pkgs.gnugrep
    ];
    text = ''
              set -u
              export LC_ALL=C

              if output="$(sbctl verify 2>&1)"; then
        rc=0
      else
        rc=$?
      fi

              printf '%s\n' "$output"

              unexpected="$(
                printf '%s\n' "$output" |
                  grep -F 'is not signed' |
                  grep -Ev '/boot/EFI/nixos/kernel-[^ ]+\.efi is not signed$' ||
                  true
              )"

              if [ -n "$unexpected" ]; then
                printf '%s\n' \
                  'ERROR: unexpected unsigned EFI artifact:' \
                  "$unexpected" >&2
                exit 1
              fi

              if [ "$rc" -ne 0 ]; then
                allowed="$(
                  printf '%s\n' "$output" |
                    grep -E '/boot/EFI/nixos/kernel-[^ ]+\.efi is not signed$' ||
                    true
                )"

                if [ -z "$allowed" ]; then
                  printf '%s\n' \
                    'ERROR: sbctl verify failed for an unexpected reason.' >&2
                  exit "$rc"
                fi
              fi

              printf '%s\n' \
                'GjallarOS boot artifact verification passed.' \
                'The external Lanzaboote kernel remains byte-identical and is hash-verified by the signed generation stub.'
    '';
  };
  secureBootOwnershipVerifier = pkgs.writeShellApplication {
    name = "gjallar-verify-secure-boot-ownership";
    runtimeInputs = [
      gjallarctlPackage
    ];

    text = ''
      set -euo pipefail
      exec gjallarctl installer secure-boot-verify-ownership ${lib.optionalString shim "--shim "}"''${1:-}"
    '';
  };

in
{
  _module.args.gjallarSecureBootArtifactVerifier = secureBootArtifactVerifier;
  _module.args.gjallarSecureBootOwnershipVerifier = secureBootOwnershipVerifier;

  environment.systemPackages = [
    secureBootArtifactVerifier
    secureBootOwnershipVerifier
  ];
}
