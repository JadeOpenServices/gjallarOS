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
  shimFiles = "${pkgs.callPackage ../../../pkgs/gjallar-shim { }}/share/shim";
  # ESP file and the pinned shim file lzbt copied it from. They carry the
  # Microsoft UEFI CA signature, not ours, so sbctl lists them as unsigned;
  # they pass only while byte-identical to the pinned package.
  shimCopies = [
    "nixos/shimx64.efi:shimx64.efi"
    "nixos/mmx64.efi:mmx64.efi"
    "BOOT/BOOTX64.EFI:shimx64.efi"
    "BOOT/mmx64.efi:mmx64.efi"
  ];
  secureBootArtifactVerifier = pkgs.writeShellApplication {
    name = "gjallar-verify-secure-boot-artifacts";
    runtimeInputs = [
      pkgs.sbctl
      pkgs.util-linux
      pkgs.coreutils
      pkgs.diffutils
    ];
    text = ''
      export LC_ALL=C

      if output="$(sbctl verify 2>&1)"; then
        rc=0
      else
        rc=$?
      fi

      printf '%s\n' "$output"

      shim_copies=" "
      ${lib.optionalString shim ''
        for pair in ${lib.concatStringsSep " " shimCopies}; do
          file=/boot/EFI/''${pair%%:*}
          [ -e "$file" ] || continue
          if ! cmp -s -- "$file" "${shimFiles}/''${pair#*:}"; then
            printf 'ERROR: %s differs from the pinned shim %s\n' "$file" "${shimFiles}/''${pair#*:}" >&2
            exit 1
          fi
          shim_copies="$shim_copies$file "
        done
      ''}

      unexpected=
      allowed=0
      while IFS= read -r line; do
        case "$line" in
          *' is not signed') ;;
          *) continue ;;
        esac
        path="''${line% is not signed}"
        path="''${path##* }"
        case "$path" in
          /boot/EFI/nixos/kernel-*.efi)
            allowed=$((allowed + 1))
            continue
            ;;
        esac
        case "$shim_copies" in
          *" $path "*)
            allowed=$((allowed + 1))
            continue
            ;;
        esac
        unexpected="$unexpected$line"$'\n'
      done <<<"$output"

      if [ -n "$unexpected" ]; then
        printf 'ERROR: unexpected unsigned EFI artifact:\n%s' "$unexpected" >&2
        exit 1
      fi

      if [ "$rc" -ne 0 ] && [ "$allowed" -eq 0 ]; then
        printf '%s\n' 'ERROR: sbctl verify failed for an unexpected reason.' >&2
        exit "$rc"
      fi

      printf '%s\n' \
        'GjallarOS boot artifact verification passed.' \
        'The external Lanzaboote kernel remains byte-identical and is hash-verified by the signed generation stub.'
      ${lib.optionalString shim ''
        printf '%s\n' 'shim and MokManager on the ESP are byte-identical to the pinned shim package.'
      ''}
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
