{ ... }:
{
  hardware.enableRedistributableFirmware = true;
  hardware.wirelessRegulatoryDatabase = true;

  services.fwupd.enable = true;
}
