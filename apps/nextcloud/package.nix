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
      rev = "c20c0fe4925607cb835a2d98f29632e954c558dd";
      sha256 = "1lzrpjvnd9zcixgg6lpf2v5s4841xzb7ynz5as9ghmylb0pzwskv";
    };

    # Upstream bounds a hydration wait by hydrationTimeoutSeconds (#72) and
    # does not mount without a client. On top of that:
    # Deliver cancellation, wait on files already hydrating, recognize
    # Nix-wrapped KIO, and take the client's PID from the socket peer.
    # Release waiters on shutdown or when the sync client goes away.
    # Count hydrationTimeoutSeconds from the last download progress, so a
    # large file on a slow link is not cut off while it still arrives.
    # Unmount when the sync client exits.
    # Keep workers free for the client while hydrations wait.
    # Log file names only in debug mode.
    # Count opens refused for lack of a slot, without names, for error reports.
    # Count programs that have a file open and note when one last read it,
    # so the client never frees up a file in use; the last close after a
    # free-up tells the client (Q79).
    patches = [
      ./patches/openvfs-hydration-liveness.patch
      ./patches/openvfs-shutdown.patch
      ./patches/openvfs-progress-deadline.patch
      ./patches/openvfs-exit-with-client.patch
      ./patches/openvfs-worker-reserve.patch
      ./patches/openvfs-quiet-socket-log.patch
      ./patches/openvfs-refusal-count.patch
      ./patches/openvfs-keep-open-files.patch
    ];

    # Upstream's socket and job tests run against the patched sources.
    doCheck = true;

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
    version = "35.0.50-openvfs";

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
