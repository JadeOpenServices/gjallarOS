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

  # The same settings the nixpkgs module writes, with the fallback target
  # passed in. See the usbguard unit below for why.
  usbguard = config.services.usbguard;
  usbguardConf = fallback: ''
    RuleFile=${pkgs.writeText "usbguard-rules" usbguard.rules}
    ImplicitPolicyTarget=${fallback}
    PresentDevicePolicy=${usbguard.presentDevicePolicy}
    PresentControllerPolicy=${usbguard.presentControllerPolicy}
    InsertedDevicePolicy=${usbguard.insertedDevicePolicy}
    RestoreControllerDeviceState=${lib.boolToString usbguard.restoreControllerDeviceState}
    DeviceManagerBackend=uevent
    IPCAllowedUsers=${lib.concatStringsSep " " usbguard.IPCAllowedUsers}
    IPCAllowedGroups=${lib.concatStringsSep " " usbguard.IPCAllowedGroups}
    IPCAccessControlFiles=/var/lib/usbguard/IPCAccessControl.d/
    DeviceRulesWithPort=${lib.boolToString usbguard.deviceRulesWithPort}
    AuditFilePath=/dev/null
  '';

  # Binds a USB SCSI device unless the media gate owns it: a removable disk
  # or optical drive, the same test as mediagate LUN.Gated. Reads only the
  # device type and the removable bit of the INQUIRY data, never the medium.
  scsiBindUngated = pkgs.writeShellScript "gjallar-scsi-bind-ungated" ''
    [[ $1 =~ ^[0-9]+:[0-9]+:[0-9]+:[0-9]+$ ]] || exit 1
    dev=/sys/bus/scsi/devices/$1
    type=$(<"$dev/type") || exit 1
    flags=$(${pkgs.coreutils}/bin/od -An -tu1 -j1 -N1 "$dev/inquiry") || exit 1
    if [[ $type == 0 || $type == 5 ]] && (( flags & 128 )); then
      exit 0
    fi
    echo "$1" > /sys/bus/scsi/drivers_probe
  '';
  scsiBindRules = ''
    ACTION=="add", SUBSYSTEM=="scsi", ENV{DEVTYPE}=="scsi_device", SUBSYSTEMS=="usb", ENV{GJALLAR_USB_SCSI}="1"
    ACTION=="add", SUBSYSTEM=="scsi", ENV{DEVTYPE}=="scsi_device", ENV{GJALLAR_USB_SCSI}!="1", RUN+="${pkgs.runtimeShell} -c 'echo %k > /sys/bus/scsi/drivers_probe'"
    ACTION=="add", SUBSYSTEM=="scsi", ENV{DEVTYPE}=="scsi_device", ENV{GJALLAR_USB_SCSI}=="1", RUN+="${scsiBindUngated} %k"
  '';

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
      ${lib.optionalString (cfg.lapseDays != null) "--lapse-days ${toString cfg.lapseDays}"} \
      --cryptsetup-binary ${pkgs.cryptsetup}/bin/cryptsetup \
      ${
        lib.concatMapStringsSep " " (luks: "--luks-device ${lib.escapeShellArg luks.device}") (
          lib.attrValues config.boot.initrd.luks.devices
        )
      } \
      --usbguard-binary ${pkgs.usbguard}/bin/usbguard
  '';
in
{
  # A stick written with dd loses partitions and keeps its old label in
  # udev (Plane 185). What the fix does and when to drop it:
  # system/fixes/systemd-partition-reread.
  imports = [ ../fixes/systemd-partition-reread ];

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
    lapseDays = lib.mkOption {
      type = lib.types.nullOr (lib.types.ints.between 1 36500);
      default =
        let
          days = settings.usbTrustLapseDays or 0;
        in
        if days > 0 then days else null;
      example = 30;
      description = "Days an external USB device may stay unconnected before its permanent trust lapses and it asks again. null never lapses. Built-in parts never lapse.";
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

      systemd.services.usbguard.preStart = lib.mkMerge [
        (lib.mkBefore ''
          rm -f -- /var/lib/usbguard/IPCAccessControl.d/${lib.escapeShellArg settings.username}
        '')
        # The fallback in /etc follows the blocking flag. A machine armed in
        # the signed state whose flag was turned off before the disarm would
        # boot with allow until the broker reconciles (loophole 23, Plane
        # 194), so USBGuard starts from a copy in /run whose fallback is
        # block while the stored state says armed or cannot be read.
        (lib.mkAfter ''
          ${usbtrustd}/bin/gjallar-usbguard-boot /var/lib/gjallar-usbtrust \
            /etc/usbguard/usbguard-daemon.conf /run/gjallar-usbguard/usbguard-daemon.conf
        '')
      ];

      # nixpkgs passes a store copy of the settings on the command line, so
      # switching between audit and enforce changed the unit and restarted
      # USBGuard. On start it applies PresentDevicePolicy with the new
      # blocking fallback and pulled a mounted card before the broker could
      # keep it (REGRESSION 734). Reading the settings from /etc (through the
      # copy in /run above) keeps the unit the same across that switch; the
      # broker sets the fallback at runtime. Any other settings change still
      # restarts USBGuard.
      environment.etc."usbguard/usbguard-daemon.conf".text = usbguardConf usbguard.implicitPolicyTarget;
      systemd.services.usbguard.restartTriggers = [
        (pkgs.writeText "usbguard-daemon-conf" (usbguardConf "set-by-broker"))
      ];

      systemd.services.usbguard.serviceConfig.RuntimeDirectory = "gjallar-usbguard";
      systemd.services.usbguard.serviceConfig.RuntimeDirectoryMode = "0700";
      systemd.services.usbguard.serviceConfig.ExecStart =
        lib.mkForce "${usbguard.package}/bin/usbguard-daemon -P -k -c /run/gjallar-usbguard/usbguard-daemon.conf";

      systemd.services.usbguard.serviceConfig.ReadWritePaths = lib.mkForce [
        "-/dev/shm"
        "-/tmp"
        "-/sys"
      ];

      # While USB protection enforces, the broker turns off automatic SCSI
      # driver binding so a medium in an allowed reader stays unread until
      # the owner opens it (gjallar-usbtrustd media gate). Every other SCSI
      # device keeps binding as soon as it appears.
      services.udev.extraRules = scsiBindRules;

      # The broker turns off SCSI autoprobe at start and the media gate binds
      # SCSI drivers later, so /sys/bus/scsi must exist before it starts.
      # Machines without SATA or USB disks at boot only get the SCSI core with
      # the first USB disk, so load it at boot.
      boot.kernelModules = [ "scsi_mod" ];

      # The broker starts late in boot. Until then a card already in a reader
      # would be read by sd (partition table) and udev (blkid) in stage 1 and
      # early stage 2 (loophole 13a, Plane 195). With blocking on, autoprobe
      # goes off before udevd runs in stage 1 and stays off across the switch
      # to stage 2. The rules above bind everything the gate does not own, so
      # SATA disks and USB disks that are not removable still boot.
      boot.initrd = lib.mkIf (cfg.enforce && config.boot.initrd.systemd.enable) {
        availableKernelModules = [ "scsi_mod" ];
        services.udev.rules = scsiBindRules;
        systemd.storePaths = [ scsiBindUngated ];
        systemd.services.gjallar-scsi-autoprobe-off = {
          description = "Keep USB storage media closed until the USB trust broker runs";
          unitConfig.DefaultDependencies = false;
          wantedBy = [ "sysinit.target" ];
          before = [
            "sysinit.target"
            "systemd-modules-load.service"
            "systemd-udevd.service"
            "systemd-udev-trigger.service"
          ];
          serviceConfig = {
            Type = "oneshot";
            RemainAfterExit = true;
          };
          script = ''
            ${pkgs.kmod}/bin/modprobe scsi_mod
            echo 0 > /sys/bus/scsi/drivers_autoprobe
          '';
        };
      };

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
          ExecStart = start;
          RuntimeDirectory = "gjallar-usbtrust";
          # The broker hands the socket to the owner UID (mode 600), so the
          # review works right after the rebuild that turns usbguard on; a
          # group would only apply at the next login.
          RuntimeDirectoryMode = "0711";
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
                # No usbReviewTechnicalView here: the review reads
                # user.config.json itself, and a unit that changes with the
                # box is restarted by the rebuild, closing the open review.
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
