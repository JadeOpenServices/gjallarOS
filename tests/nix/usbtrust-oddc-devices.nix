# The USB trust broker gets the built-in USB parts from `oddc devices --json`
# at the pinned ODDC release, built for the machine's model. The ZBook x2 G4
# lists its keyboard with a root port; the FW13 AMD 7040 has no keyboard fact
# (ODDC answer to 102), so it must not grow one by accident. The keyboard
# gate tier follows from that: ZBook usb-keyboard, FW13 none. A devices file
# that puts the keyboard on another port than the resolved view must fail
# the build.
{
  nixpkgs,
  system,
  oddc,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};

  machine =
    model: extra:
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
        extra
      ];
    }).config;

  zbook = machine "model/hp/zbook-x2-g4" { };
  fw13 = machine "model/framework/laptop-13-amd-ryzen-7040" { };

  # SYNTHETIC: the ZBook devices file with the keyboard moved to port 3.
  movedKeyboard =
    pkgs.runCommand "usbtrust-oddc-devices-moved-keyboard" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq '.devices |= map(if .path == "hardware.input.keyboard.primary" then .usbPort.port = 3 else . end)' \
          ${zbook.services.gjallar-usbtrust.oddcDevices} > $out
      '';
  zbookMoved = machine "model/hp/zbook-x2-g4" {
    services.gjallar-usbtrust.oddcDevices = movedKeyboard;
  };
  portCheck =
    config:
    lib.findFirst (
      c: lib.hasSuffix "gjallar-usbtrust-keyboard-port-check" c.name
    ) (throw "no keyboard port check") config.system.checks;
  inherit (nixpkgs) lib;

in
assert !zbook.services.usbguard.restoreControllerDeviceState;
pkgs.runCommand "usbtrust-oddc-devices"
  {
    nativeBuildInputs = [ pkgs.jq ];
    zbook = zbook.services.gjallar-usbtrust.oddcDevices;
    fw13 = fw13.services.gjallar-usbtrust.oddcDevices;
    zbookStart = zbook.systemd.services.usb-trust-broker.serviceConfig.ExecStart;
    fw13Start = fw13.systemd.services.usb-trust-broker.serviceConfig.ExecStart;
    zbookGate = zbook.environment.etc."gjallar/usb-keyboard-gate.json".text;
    fw13Gate = fw13.environment.etc."gjallar/usb-keyboard-gate.json".text;
    zbookCheck = portCheck zbook;
    fw13Check = portCheck fw13;
    movedCheck = pkgs.testers.testBuildFailure (portCheck zbookMoved);
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
    jq -e '. == {tier: "usb-keyboard", reason: "keyboard-on-usb", enforce: false, earlyGate: false,
      keyboard: {deviceId: "1fc9:0088", attachment: "detachable", name: .keyboard.name}}
      and (.keyboard.name | type == "string")' <<<"$zbookGate"
    jq -e '. == {tier: "none", reason: "no-keyboard-facts", enforce: false, earlyGate: false, keyboard: null}' <<<"$fw13Gate"
    test -e "$zbookCheck" && test -e "$fw13Check"
    grep -qF "disagree on the USB keyboard" "$movedCheck/testBuildFailure.log"
    touch $out
  ''
