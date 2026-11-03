{ settings, ... }:

{
  # Off only for people who run a firewall of their own (gjallarHorn
  # switch, Plane 216). The recovery system forces it on.
  networking.firewall.enable = settings.firewallEnable or true;
}
