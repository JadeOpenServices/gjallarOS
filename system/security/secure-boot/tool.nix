{
  lib,
  pkgs,
  gjallarctlPackage,
  gjallarSecureBootArtifactVerifier,
  ...
}:
let
  secureBootTool = pkgs.writeShellApplication {
    name = "gjallar-secure-boot";
    runtimeInputs = [
      pkgs.sbctl
      pkgs.systemd
      pkgs.fwupd
      gjallarctlPackage
      gjallarSecureBootArtifactVerifier
    ];

    text = ''
      set -euo pipefail

      case "''${1:-status}" in
        status)
          bootctl status
          printf '\n'
          sudo sbctl status
          printf '\nSigned boot artifacts:\n'
          sudo gjallar-verify-secure-boot-artifacts
          ;;
        enroll)
          sudo gjallarctl installer secure-boot-enroll
          ;;
        firmware)
          fwupdmgr get-devices
          ;;
        *)
          printf '%s\n' \
            'Usage: gjallar-secure-boot {status|enroll|firmware}'
          exit 2
          ;;
      esac
    '';
  };
in
{
  # What the pinned shim is (signer CAs, Authenticode hashes, SBAT), so
  # `gjallarctl secure-boot setup` can check db and dbx before it picks the
  # shim path, also while Secure Boot is still off (Plane 293).
  environment.etc."gjallar/shim.json" = lib.mkIf pkgs.stdenv.hostPlatform.isx86_64 {
    source = "${(pkgs.callPackage ../../../pkgs/gjallar-shim { }).facts}/share/shim/shim.json";
  };

  environment.systemPackages = [
    secureBootTool
    # `gjallarctl secure-boot setup` creates and archives the keys while
    # Secure Boot is still off, before any of the gated services exist; it
    # finds these through PATH (privilegedTool).
    pkgs.sbctl
    pkgs.openssl
    (pkgs.writeTextDir "share/zsh/site-functions/_gjallar-secure-boot" ''
      #compdef gjallar-secure-boot
      local -a cmds=(
        'status:boot, key and signed artifact state'
        'enroll:enroll the GjallarOS Secure Boot keys'
        'firmware:list firmware devices'
      )
      _describe -t commands 'gjallar-secure-boot command' cmds
    '')
  ];
}
