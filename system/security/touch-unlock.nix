{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  # Tablets and convertibles may have no keyboard at the LUKS prompt.
  # Plymouth has no on-screen keyboard (Fedora bug 1411026, closed unfixed),
  # so unl0kr takes the password prompt there. TPM unlock never asks, so a
  # normal boot stays on Plymouth.
  enabled = (settings.touchscreenEnable or false) && config.boot.initrd.luks.devices != { };

  package = pkgs.buffybox;
  plymouth = config.boot.plymouth.package;

  # unl0kr ships these layouts only.
  layout =
    if
      builtins.elem (settings.keyboardLayout or "us") [
        "us"
        "de"
        "es"
        "fr"
      ]
    then
      settings.keyboardLayout
    else
      "us";

  unl0krConf = (pkgs.formats.ini { }).generate "unl0kr.conf" {
    keyboard = {
      # Hidden while a hardware keyboard is attached.
      autohide = true;
      inherit layout;
    };
    theme.default = "breezy-dark";
  };

  # unl0kr and Plymouth both want the display. Hand it over only when a
  # password is actually asked.
  quitPlymouth = pkgs.writeShellScript "unl0kr-quit-plymouth" ''
    ${plymouth}/bin/plymouth --ping || exit 0
    ${plymouth}/bin/plymouth quit --retain-splash
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
      ${plymouth}/bin/plymouth --ping || exit 0
      ${pkgs.coreutils}/bin/sleep 0.1
    done
  '';

  # Upstream units skip themselves while Plymouth runs.
  plymouthHandoff = {
    unitConfig.ConditionPathExists = "";
  };
in
{
  # Same pieces as nixpkgs boot.initrd.unl0kr, which warns on every rebuild
  # when Plymouth is on; the handoff below is what makes the pair work.
  config = lib.mkIf enabled {
    # Loaded at the start of stage 1, so it is there when the panel shows up.
    # Loaded on demand, hid-generic takes the panel first and hands it to
    # hid-multitouch a moment later, while unl0kr is already starting, and
    # touch stays dead at the prompt (ZBook x2 G4, 2026-10-11). The pen does
    # not work there either way: unl0kr reads keys, pointer and touch only.
    boot.initrd.kernelModules = [ "hid-multitouch" ];

    boot.initrd.availableKernelModules = [
      "hid-multitouch"
      "hid-generic"
      "usbhid"
      "i2c-designware-core"
      "i2c-designware-platform"
      "i2c-hid-acpi"
      # I2C touchscreens on Intel sit behind the LPSS I2C controllers and use
      # GPIO interrupts. Without these the controller never shows up in stage
      # 1 and the panel stays dead at the LUKS prompt (book1, Kaby Lake,
      # 2026-10-10). udev loads only the ones that match the hardware.
      "intel-lpss"
      "intel-lpss-pci"
      "intel-lpss-acpi"
      "pinctrl-sunrisepoint"
      "pinctrl-broxton"
      "pinctrl-geminilake"
      "pinctrl-cannonlake"
      "pinctrl-icelake"
      "pinctrl-jasperlake"
      "pinctrl-elkhartlake"
      "pinctrl-tigerlake"
      "pinctrl-alderlake"
      "pinctrl-meteorlake"
      # Wacom panels and pens report through their own HID driver.
      "wacom"
      "usbtouchscreen"
      # Touchscreens in VMs.
      "virtio_input"
      "evdev"
    ];

    boot.initrd.systemd = {
      contents."/etc/unl0kr.conf".source = unl0krConf;
      storePaths = [
        pkgs.libinput
        pkgs.libinput.out
        pkgs.xkeyboard_config
        (lib.getExe' package "unl0kr")
        "${package}/libexec/unl0kr-agent"
        quitPlymouth
      ];
      packages = [ package ];

      paths.unl0kr-agent = plymouthHandoff // {
        wantedBy = [ "local-fs-pre.target" ];
      };
      services.unl0kr-agent = plymouthHandoff // {
        serviceConfig.ExecStartPre = quitPlymouth;
      };

      # One agent per prompt: Plymouth's would race unl0kr for the answer.
      paths.systemd-ask-password-plymouth.enable = false;
    };
  };
}
