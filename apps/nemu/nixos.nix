{
  config,
  lib,
  pkgs,
  settings,
  ...
}:

let
  gpuContract = lib.attrByPath [
    "policy"
    "virtualization"
    "gpu"
  ] { } config.oddc.resolved;

  passthrough = gpuContract.passthrough or { };
  passthroughMode = passthrough.mode or "disabled";
  passthroughValidated = passthrough.validated or false;

  passthroughAllowed =
    passthroughValidated
    && builtins.elem passthroughMode [
      "sriov"
      "mdev"
      "vfio-exclusive"
    ];
in
{
  imports = [
    ./module.nix
    ./spice.nix
  ];

  config = lib.mkIf config.gjallar.apps.nemu.enable {
    warnings = lib.optional (passthroughMode != "disabled" && !passthroughAllowed) ''
      ODDC GPU passthrough policy is not validated. Physical GPU passthrough
      is disabled and host GPU ownership is preserved.
    '';

    environment.etc."nemu/gpu-policy.json".text = builtins.toJSON {
      genericAcceleration = "auto";
      physicalPassthrough = if passthroughAllowed then passthroughMode else "disabled";
      source = if passthroughAllowed then "oddc-validated" else "safe-default";
    };

    environment.systemPackages = with pkgs; [
      virt-viewer
      tigervnc
      qemu
      samba
    ];

    # The upstream module predates the required wrapper owner and group.
    security.wrappers.nemu = {
      owner = "root";
      group = "root";
    };

    # Older ~/.nemu.cfg copies hold viewer and QEMU paths from the store that a
    # garbage collection removes; point them at the system profile.
    home-manager.sharedModules = [
      (
        { lib, ... }:
        {
          home.activation.nemuStablePaths = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
            cfg="$HOME/.nemu.cfg"
            if [ -f "$cfg" ] && [ ! -L "$cfg" ] && grep -q -E '^(vnc_bin|spice_bin|qemu_bin_path) = /nix/store/' "$cfg"; then
              run ${pkgs.gnused}/bin/sed -i -E \
                -e 's|^vnc_bin = /nix/store/.*|vnc_bin = /run/current-system/sw/bin/vncviewer|' \
                -e 's|^spice_bin = /nix/store/.*|spice_bin = /run/current-system/sw/bin/remote-viewer|' \
                -e 's|^qemu_bin_path = /nix/store/.*|qemu_bin_path = /run/current-system/sw/bin|' \
                "$cfg"
            fi
          '';
        }
      )
    ];

    programs.nemu = {
      package = pkgs._nemu;
      enable = true;
      vhostNetGroup = "nemu-vhost";
      macvtapGroup = "nemu-vhost";
      users = {
        ${settings.username}.autoAddVeth = false;
      };
    };
  };
}
