{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
let
  cfg = config.services.gjallar-usbtrust;
  configuredTPMHandle = settings.usbTrustTpmHandle or "";
  usbtrustd = pkgs.callPackage ../../pkgs/gjallar-usbtrustd { };
  resolved = pkgs.writeText "gjallar-usbtrust-oddc.json" (builtins.toJSON config.oddc.resolved);

  # zenity --password has no --text option and silently drops it, so the
  # reason line needs --entry --hide-text. Output is the same: text + newline.
  usbTrustAskpass = pkgs.writeShellScript "gjallar-usbtrust-askpass" ''
    exec ${pkgs.zenity}/bin/zenity \
      --entry \
      --hide-text \
      --title="Authorize USB device" \
      --text="Fingerprint authentication was unavailable, failed, or timed out.\nEnter your password to authorize this USB decision."
  '';
  start = pkgs.writeShellScript "gjallar-usbtrustd-start" ''
    ownerUID="$(${pkgs.coreutils}/bin/id -u ${lib.escapeShellArg settings.username})"
    exec ${usbtrustd}/bin/gjallar-usbtrustd \
      --socket /run/gjallar-usbtrust/control.sock \
      --owner-uid "$ownerUID" \
      --oddc-resolved ${resolved} \
      ${lib.optionalString (cfg.oddcDevices != null) "--oddc-devices ${cfg.oddcDevices}"} \
      --oddc-model ${
        lib.escapeShellArg (if config.oddc.device == null then "" else config.oddc.device)
      } \
      --state-dir /var/lib/gjallar-usbtrust \
      --request-timeout 30s \
      ${lib.optionalString (cfg.tpmHandle != null) "--tpm-handle ${lib.escapeShellArg cfg.tpmHandle}"} \
      ${lib.optionalString cfg.enforce "--enforce"} \
      --cryptsetup-binary ${pkgs.cryptsetup}/bin/cryptsetup \
      ${lib.concatMapStringsSep " " (
        luks: "--luks-device ${lib.escapeShellArg luks.device}"
      ) (lib.attrValues config.boot.initrd.luks.devices)} \
      --usbguard-binary ${pkgs.usbguard}/bin/usbguard
  '';
in
{
  options.services.gjallar-usbtrust = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = settings.usbguardEnable or false;
      description = "Run the machine-local USB trust broker and observation backend.";
    };
    enforce = lib.mkOption {
      type = lib.types.bool;
      default = settings.usbTrustEnforce or false;
      description = "Arm USB blocking. The first arming enrolls ODDC internal devices; once armed, only the disk encryption passphrase (gjallarctl usb disarm) turns it off again.";
    };
    oddcDevices = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Output of `oddc devices --json` for the selected model. Built-in parts it lists with a usbPort are then judged by their root port.";
    };
    tpmHandle = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = if configuredTPMHandle == "" then null else configuredTPMHandle;
      example = "0x81000042";
      description = "Explicitly reserved TPM persistent P-256 signing handle; required for permanent trust.";
    };
  };

  config = lib.mkMerge [
    { services.udisks2.enable = true; }
    (lib.mkIf cfg.enable {
      assertions = [
        {
          assertion = config.oddc.device != null;
          message = "USB trust requires a selected ODDC model.";
        }
        {
          assertion = cfg.tpmHandle == null || builtins.match "^0x810[0-9a-fA-F]{5}$" cfg.tpmHandle != null;
          message = "USB trust TPM handle must be a reserved owner handle in 0x81000000..0x810fffff.";
        }
        {
          assertion = !cfg.enforce || cfg.tpmHandle != null;
          message = "USB trust enforcement requires a configured TPM signing handle; enroll and audit machine identities before enabling enforcement.";
        }
      ];
      users.groups.usb-trust-access = { };
      users.users.${settings.username}.extraGroups = [ "usb-trust-access" ];

      services.usbguard = {
        enable = true;
        dbus.enable = false;
        IPCAllowedUsers = lib.mkForce [ "root" ];
        IPCAllowedGroups = lib.mkForce [ ];
        rules = "";
        implicitPolicyTarget = if cfg.enforce then "block" else "allow";
        presentDevicePolicy = "apply-policy";
        insertedDevicePolicy = "apply-policy";
        presentControllerPolicy = "keep";
        restoreControllerDeviceState = true;
        deviceRulesWithPort = false;
      };

      systemd.services.usbguard.preStart = lib.mkBefore ''
        rm -f -- /var/lib/usbguard/IPCAccessControl.d/${lib.escapeShellArg settings.username}
      '';

      systemd.services.usbguard.serviceConfig.ReadWritePaths = lib.mkForce [
        "-/dev/shm"
        "-/tmp"
        "-/sys"
      ];

      # While USB protection enforces, the broker turns off automatic SCSI
      # driver binding so a medium in an allowed reader stays unread until
      # the owner opens it (gjallar-usbtrustd media gate). Disks that are not
      # on USB keep binding as soon as they appear.
      services.udev.extraRules = ''
        ACTION=="add", SUBSYSTEM=="scsi", ENV{DEVTYPE}=="scsi_device", SUBSYSTEMS=="usb", ENV{GJALLAR_USB_SCSI}="1"
        ACTION=="add", SUBSYSTEM=="scsi", ENV{DEVTYPE}=="scsi_device", ENV{GJALLAR_USB_SCSI}!="1", RUN+="${pkgs.runtimeShell} -c 'echo %k > /sys/bus/scsi/drivers_probe'"
      '';

      # The broker turns off SCSI autoprobe at start and the media gate binds
      # SCSI drivers later, so /sys/bus/scsi must exist before it starts.
      # Machines without SATA or USB disks at boot only get the SCSI core with
      # the first USB disk, so load it at boot.
      boot.kernelModules = [ "scsi_mod" ];

      systemd.services.usb-trust-broker = {
        description = "USB trust broker";
        wantedBy = [
          "multi-user.target"
          "usbguard.service"
        ];
        requires = [ "usbguard.service" ];
        bindsTo = [ "usbguard.service" ];
        partOf = [ "usbguard.service" ];
        after = [
          "usbguard.service"
          "systemd-modules-load.service"
        ];
        path = [ pkgs.tpm2-tools ];
        serviceConfig = {
          Type = "simple";
          Environment = [ "TPM2TOOLS_TCTI=device:/dev/tpmrm0" ];
          User = "root";
          Group = "usb-trust-access";
          ExecStart = start;
          RuntimeDirectory = "gjallar-usbtrust";
          RuntimeDirectoryMode = "0750";
          StateDirectory = "gjallar-usbtrust";
          StateDirectoryMode = "0700";
          UMask = "0077";
          Restart = "on-failure";
          RestartSec = "2s";
          NoNewPrivileges = true;
          PrivateTmp = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          ProtectKernelTunables = true;
          # The media gate binds and unbinds SCSI upper drivers.
          ReadWritePaths = [ "/sys/bus/scsi" ];
          ProtectKernelModules = true;
          ProtectControlGroups = true;
          RestrictSUIDSGID = true;
          LockPersonality = true;
        };
      };
      home-manager.users.${settings.username} = { pkgs, ... }: {
        systemd.user.services = lib.optionalAttrs (!(settings.endpointManagedDevice or false)) {
          usb-device-review = {
            Unit = {
              Description = "USB device review";
              After = [ "hyprland-session.target" ];
              PartOf = [ "hyprland-session.target" ];
            };
            Service = {
              ExecStart = "${pkgs.callPackage ../../pkgs/gjallarctl { }}/bin/gjallarctl usb review";
              Environment = [
                "PATH=${lib.makeBinPath [ pkgs.zenity ]}"
                "SUDO_ASKPASS=${usbTrustAskpass}"
                # Fallback start view; the review reads user.config.json itself.
                "GJALLAR_USB_REVIEW_VIEW=${
                  if settings.usbReviewTechnicalView or false then "technical" else "simple"
                }"
              ];
              Restart = "on-failure";
              RestartSec = 3;
            };
            Install.WantedBy = [ "hyprland-session.target" ];
          };
        };
      };
    })
  ];
}
