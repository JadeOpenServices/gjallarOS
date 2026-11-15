# Printing on this computer keeps every port closed. ipp-usb turns avahi on,
# and avahi opens UDP 5353 unless told not to (L57); only the network choice
# may open it.
{
  nixpkgs,
  system,
  printingModule,
}:
let
  pkgs = nixpkgs.legacyPackages.${system};
  firewall =
    printingEnable: networkPrintingEnable:
    let
      config =
        (nixpkgs.lib.nixosSystem {
          inherit system;
          specialArgs.settings = { inherit printingEnable networkPrintingEnable; };
          modules = [
            printingModule
            {
              system.stateVersion = "26.05";
              boot.loader.grub.enable = false;
              fileSystems."/" = {
                device = "/dev/null";
                fsType = "ext4";
              };
            }
          ];
        }).config;
    in
    {
      avahi = config.services.avahi.enable;
      udp = config.networking.firewall.allowedUDPPorts;
      tcp = config.networking.firewall.allowedTCPPorts;
    };
  off = firewall false false;
  usb = firewall true false;
  network = firewall true true;
in
assert
  off == {
    avahi = false;
    udp = [ ];
    tcp = [ ];
  };
assert
  usb == {
    avahi = true;
    udp = [ ];
    tcp = [ ];
  };
assert
  network == {
    avahi = true;
    udp = [ 5353 ];
    tcp = [ ];
  };
pkgs.runCommand "gjallar-printing-firewall-check" { } "touch $out"
