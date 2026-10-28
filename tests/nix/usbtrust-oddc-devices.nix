# The USB trust broker gets the built-in USB parts from `oddc devices --json`
# at the pinned ODDC release, built for the machine's model. The ZBook x2 G4
# lists its keyboard with a root port; the FW13 AMD 7040 has no keyboard fact
# (ODDC answer to 102), so it must not grow one by accident.
{
  nixpkgs,
  system,
  oddc,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};

  machine =
    model:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      specialArgs.settings = {
        username = "testuser";
        usbguardEnable = true;
        usbTrustEnforce = false;
        usbTrustTpmHandle = "0x81000042";
      };
      modules = [
        oddc.nixosModules.default
        ../../system/services/removable-media.nix
        (
          { lib, ... }:
          {
            options.home-manager.users = lib.mkOption {
              type = lib.types.attrsOf lib.types.raw;
              default = { };
            };
            config = {
              oddc.catalog = oddc;
              oddc.device = model;
              boot.loader.grub.enable = false;
              fileSystems."/" = {
                device = "none";
                fsType = "tmpfs";
              };
              system.stateVersion = "26.05";
            };
          }
        )
      ];
    }).config;

  zbook = machine "model/hp/zbook-x2-g4";
  fw13 = machine "model/framework/laptop-13-amd-ryzen-7040";

in
assert !zbook.services.usbguard.restoreControllerDeviceState;
pkgs.runCommand "usbtrust-oddc-devices"
  {
    nativeBuildInputs = [ pkgs.jq ];
    zbook = zbook.services.gjallar-usbtrust.oddcDevices;
    fw13 = fw13.services.gjallar-usbtrust.oddcDevices;
    zbookStart = zbook.systemd.services.usb-trust-broker.serviceConfig.ExecStart;
    fw13Start = fw13.systemd.services.usb-trust-broker.serviceConfig.ExecStart;
  }
  ''
    grep -qF -- "--oddc-devices $zbook " "$zbookStart"
    grep -qF -- "--oddc-devices $fw13 " "$fw13Start"
    jq -e '.model == "model/hp/zbook-x2-g4"' "$zbook"
    jq -e '[.devices[] | select(.path == "hardware.input.keyboard.primary")]
      | length == 1 and .[0].deviceId == "1fc9:0088" and .[0].attachment == "detachable"
        and .[0].usbPort.controller == "8086:9d2f" and .[0].usbPort.hub == "usb2"
        and .[0].usbPort.port == 4' "$zbook"
    jq -e '.model == "model/framework/laptop-13-amd-ryzen-7040"' "$fw13"
    jq -e '[.devices[] | select(.path | startswith("hardware.input.keyboard."))] | length == 0' "$fw13"
    touch $out
  ''
