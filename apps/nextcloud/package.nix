# The OpenVFS Nextcloud client. home.nix and the flake package
# nextcloud-client-openvfs both import this file with the system pkgs, so the
# CI cache builds the same derivation that machines install.
{ pkgs }:
let
  openvfs = pkgs.stdenv.mkDerivation {
    pname = "openvfs";
    version = "0.1.0";

    # Qt6 is present only so ECM can query qtpaths during configuration.
    # OpenVFS itself does not require Qt application wrapping.
    dontWrapQtApps = true;

    src = pkgs.fetchFromGitHub {
      owner = "opencloud-eu";
      repo = "openvfs";
      rev = "4c2317b2cf73a28503d4c5f190bd3f1dd8355cf5";
      sha256 = "071kc5w2d52835np2k2xan0pk6vc3kbf98w4klxlrvp8wja54ksv";
    };

    # Bound hydration waits, deliver cancellation, and recognize Nix-wrapped KIO.
    # Release waiters on shutdown or when the sync client goes away.
    # Time a hydration out only when its download stops making progress.
    patches = [
      ./patches/openvfs-hydration-liveness.patch
      ./patches/openvfs-shutdown.patch
      ./patches/openvfs-progress-deadline.patch
    ];

    nativeBuildInputs = [
      pkgs.cmake
      pkgs.kdePackages.extra-cmake-modules
      pkgs.kdePackages.qtbase
      pkgs.pkg-config
    ];

    buildInputs = [
      pkgs.fuse3
      pkgs.nlohmann_json
    ];
  };

  clientSource = import ./client-source.nix { inherit (pkgs) fetchFromGitHub replaceVars; };

  client = pkgs.nextcloud-client.overrideAttrs (old: {
    version = "34.0.50-openvfs";

    inherit (clientSource) src patches;

    buildInputs = (old.buildInputs or [ ]) ++ [
      openvfs
      pkgs.kdsingleapplication
    ];

    cmakeFlags = (old.cmakeFlags or [ ]) ++ [
      "-DENFORCE_SINGLE_ACCOUNT=ON"
      # No classic-sync choice, and a classic folder becomes virtual files
      # on the next client start.
      "-DENFORCE_VIRTUAL_FILES_SYNC_FOLDER=ON"
    ];

    postPatch = (old.postPatch or "") + ''
      if ! grep -Fq '"--overrideserverurl"' src/gui/application.cpp \
        || ! grep -Fq '"--overridelocaldir"' src/gui/application.cpp
      then
        echo "Nextcloud interactive wizard override CLI changed" >&2
        exit 1
      fi

      if ! grep -Eq 'option\([[:space:]]*ENFORCE_SINGLE_ACCOUNT' NEXTCLOUD.cmake; then
        echo "Nextcloud no longer exposes ENFORCE_SINGLE_ACCOUNT" >&2
        exit 1
      fi
    '';

    postInstall = (old.postInstall or "") + ''
      vfs_plugin="$(${pkgs.findutils}/bin/find "$out" \
        \( -type f -o -type l \) \
        -name 'nextcloudsync_vfs_*.so' \
        -print -quit)"

      if [ -z "$vfs_plugin" ]; then
        echo "Nextcloud build has no Linux VFS plugin; refusing classic-sync fallback" >&2
        exit 1
      fi

      echo "Nextcloud Linux VFS plugin: $vfs_plugin"
    '';
  });
in
{
  inherit openvfs client;
}
