# Local packages under the `_` prefix, kept apart from nixpkgs names. The
# other .nix files here are picked up by pkgs/lib/overlays.nix as well.
final: prev: {
  # nemu with its VMs and database under XDG data instead of ~/nemu_vm and
  # ~/.nemu.db. nemu copies its sample config to ~/.nemu.cfg once, so the
  # viewer and QEMU paths in it point at the system profile: store paths
  # there would go stale after a garbage collection.
  _nemu = prev.nemu.overrideAttrs (old: {
    cmakeFlags = old.cmakeFlags ++ [
      "-DNM_DEFAULT_VMDIR=.local/share/nemu/vms"
      "-DNM_DEFAULT_DBFILE=.local/share/nemu/nemu.db"
    ];
    postPatch = old.postPatch + ''
      sed -i -E \
        -e 's|^vnc_bin = .*|vnc_bin = /run/current-system/sw/bin/vncviewer|' \
        -e 's|^spice_bin = .*|spice_bin = /run/current-system/sw/bin/remote-viewer|' \
        -e 's|^qemu_bin_path = .*|qemu_bin_path = /run/current-system/sw/bin|' \
        nemu.cfg.sample
      for key in vnc_bin spice_bin qemu_bin_path; do
        grep -q "^$key = /run/current-system/sw/bin" nemu.cfg.sample
      done
    '';
  });
}
