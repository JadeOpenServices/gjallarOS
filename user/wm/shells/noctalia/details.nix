{ config, pkgs,
  inputs,
  lib,
  ...
}:

let
  noctalia = lib.getExe config.programs.noctalia.package;
  ipc = "${pkgs.writeShellScript "gjallar-noctalia-ipc" ''
    if ${noctalia} msg status >/dev/null 2>&1; then
      ${noctalia} msg "$@" >/dev/null 2>&1 || true
    fi
    exit 0
  ''}";
in
{
  launchCommands = [
    "${noctalia}"
  ];

  binds = {
    launcher = "${ipc} panel-toggle launcher";
    controlCenter = "${ipc} panel-toggle control-center";

    # Held volume keys repeat about 7 times a second (ZBook, ODDC 26.2).
    volumeUp = "${ipc} volume-up 5";
    volumeDown = "${ipc} volume-down 5";
    volumeMute = "${ipc} volume-mute";
    brightnessUp = "${ipc} brightness-up current 5";
    brightnessDown = "${ipc} brightness-down current 5";
    lock = "${ipc} session lock";
    screenshot = "${ipc} screenshot-region";
    micMute = "${ipc} mic-mute";
    mediaNext = "${ipc} media next";
    mediaPrev = "${ipc} media previous";
    mediaToggle = "${ipc} media toggle";
  };
}
