{ lib, options, pkgs, ... }:
{
  config = lib.mkIf (!(options ? gjallar && options.gjallar ? installerResume)) {
    systemd.services.installer-resume = {
      description = "Resume the gjallarOS installer after a planned or interrupted reboot";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "local-fs.target" "network-online.target" ];
      before = [ "display-manager.service" "greetd.service" ];
      path = [ "/run/wrappers" "/run/current-system/sw" pkgs.sbctl pkgs.efibootmgr pkgs.gptfdisk pkgs.parted ];
      unitConfig = {
        ConditionPathExists = [ "|/var/lib/gjallarOS/installer-resume/pending.json" "|/var/lib/gjallarOS/installer-resume/active.json" ];
        ConditionFileIsExecutable = "/var/lib/gjallarOS/installer-resume/gjallar-installer";
      };
      serviceConfig = {
        Type = "oneshot";
        UMask = "0077";
        TimeoutStartSec = 0;
        StandardInput = "tty";
        StandardOutput = "tty";
        StandardError = "journal+console";
        TTYPath = "/dev/tty1";
        TTYReset = true;
        TTYVHangup = true;
        ExecStartPre = [ "-${pkgs.plymouth}/bin/plymouth quit" "-${pkgs.kbd}/bin/chvt 1" ];
        ExecStart = "/var/lib/gjallarOS/installer-resume/gjallar-installer --resume-transaction /var/lib/gjallarOS/installer-resume";
      };
    };
  };
}
