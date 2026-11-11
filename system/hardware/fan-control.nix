# ODDC runs the fw-fanctrl backend on models with fan policy; gjallarOS
# supplies the policy controller that applies its thresholds.
{ ... }:
{
  oddc.fanControl.controller.command = "/run/current-system/sw/bin/gjallarctl fan controller";

  systemd.tmpfiles.rules = [
    "d /run/gjallarOS 0755 root root - -"
  ];
}
