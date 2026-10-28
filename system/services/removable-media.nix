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

  # Built-in USB parts with their root port come only from the ODDC CLI at
  # the pinned release (ODDC contract, answer to 102). The ODDC module puts
  # the CLI in systemPackages; there is no option that names it.
  oddcPackages = builtins.filter (p: (p.pname or "") == "oddc") config.environment.systemPackages;
  oddcPackage =
    if builtins.length oddcPackages == 1 then
      builtins.head oddcPackages
    else
      throw "USB trust: expected one oddc package in environment.systemPackages, found ${toString (builtins.length oddcPackages)}";
  oddcDevicesFile =
    pkgs.runCommand "gjallar-usbtrust-oddc-devices.json" { nativeBuildInputs = [ oddcPackage ]; }
      ''
        oddc devices --json --root ${config.oddc.catalog} --device ${lib.escapeShellArg config.oddc.device} > $out
      '';

  # Until the broker starts, every USB port takes any device (Plane 194 part
  # 2). What can close that gap depends on the keyboard the owner needs for
  # the disk unlock, and only ODDC knows it: one keyboard on a USB root port
  # can be let in by that port alone. No keyboard fact, or a keyboard that
  # is not on USB, has no early gate yet. The gate itself:
  # usb-keyboard-gate.nix.
  keyboardSlots = lib.filterAttrs (_: lib.isAttrs) (
    config.oddc.resolved.hardware.input.keyboard or { }
  );
  usbKeyboards = lib.filterAttrs (_: slot: slot ? usbPort) keyboardSlots;
  keyboardGate =
    let
      tier =
        if keyboardSlots == { } then
          {
            tier = "none";
            reason = "no-keyboard-facts";
          }
        else if lib.length (lib.attrNames keyboardSlots) == 1 && usbKeyboards == keyboardSlots then
          {
            tier = "usb-keyboard";
            reason = "keyboard-on-usb";
          }
        else if usbKeyboards == { } then
          {
            tier = "none";
            reason = "keyboard-not-on-usb";
          }
        else
          {
            tier = "none";
            reason = "several-keyboards";
          };
      keyboard = lib.head (lib.attrValues usbKeyboards);
    in
    tier
    // {
      inherit (cfg) enforce;
      earlyGate =
        cfg.enforce
        && tier.tier == "usb-keyboard"
        && config.boot.initrd.systemd.enable
        && cfg.oddcDevices != null;
      keyboard =
        if tier.tier == "usb-keyboard" then
          {
            deviceId = keyboard.deviceId or null;
            attachment = keyboard.attachment or null;
            name = keyboard.name or null;
          }
        else
          null;
    };

  # The resolved view names the keyboard's controller only by reference, so
  # the port the gate opens comes from the devices file. Both come from the
  # same pinned catalogue; a build where they disagree on which keyboard
  # sits on which port fails here instead of gating the wrong port.
  keyboardPortCheck =
    let
      expected = lib.mapAttrsToList (slot: kb: {
        path = "hardware.input.keyboard.${slot}";
        deviceId = kb.deviceId or null;
        attachment = kb.attachment or null;
        hub = kb.usbPort.hub or null;
        port = kb.usbPort.port or null;
      }) usbKeyboards;
    in
    pkgs.runCommand "gjallar-usbtrust-keyboard-port-check"
      {
        nativeBuildInputs = [ pkgs.jq ];
        expected = builtins.toJSON expected;
        devices = cfg.oddcDevices;
      }
      ''
        jq -e --argjson expected "$expected" '
          [.devices[] | select(.path | startswith("hardware.input.keyboard.")) | select(.usbPort)]
          | (map({path, deviceId, attachment, hub: .usbPort.hub, port: .usbPort.port}) | sort_by(.path))
              == ($expected | sort_by(.path))
            and all(.usbPort.controller | type == "string" and test("^[0-9a-f]{4}:[0-9a-f]{4}$"))
        ' "$devices" > /dev/null || {
          echo "USB trust: oddc devices and the resolved ODDC view disagree on the USB keyboard" >&2
          exit 1
        }
        touch $out
      '';

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

  # udisks hands UDISKS_ICON_NAME to its clients as the drive's icon. Without
  # it udiskie shows one generic icon for every drive, while Dolphin (KDE
  # Solid) picks its own from the same udisks facts. These lines give udisks
  # the icon Solid would pick, so both show the same one (Plane 233). Discs
  # keep Solid's own icons, which also tell the disc type. The rules run
  # after 80-udisks2.rules, which sets ID_DRIVE_*, and never replace an icon
  # a device rule already set. A USB disk's card kind comes from
  # gjallar-storage-icon, the classifier the media notices use: udisks only
  # knows model strings like "*Reader*SD*", while reader slots often call
  # themselves "SD/MMC" or "MS-Pro". The udisks per-device flags still refine
  # a slot the classifier only knows as some memory card.
  storageIconRules = ''
    SUBSYSTEM!="block", GOTO="gjallar_storage_icon_end"
    ENV{UDISKS_ICON_NAME}!="", GOTO="gjallar_storage_icon_end"
    ENV{ID_CDROM}=="1", GOTO="gjallar_storage_icon_end"
    KERNEL=="mmcblk[0-9]*", SUBSYSTEMS=="mmc", ENV{UDISKS_ICON_NAME}="media-flash-sd-mmc", GOTO="gjallar_storage_icon_end"
    KERNEL=="msblk[0-9]*|mspblk[0-9]*", SUBSYSTEMS=="memstick", ENV{UDISKS_ICON_NAME}="media-flash-memory-stick", GOTO="gjallar_storage_icon_end"
    ENV{ID_BUS}!="usb", GOTO="gjallar_storage_icon_end"
    ENV{DEVTYPE}=="disk", IMPORT{program}="${usbtrustd}/bin/gjallar-storage-icon $devpath"
    ENV{DEVTYPE}=="partition", IMPORT{parent}="GJALLAR_STORAGE_KIND"
    ENV{GJALLAR_STORAGE_KIND}=="sd-card", ENV{UDISKS_ICON_NAME}="media-flash-sd-mmc", GOTO="gjallar_storage_icon_end"
    ENV{GJALLAR_STORAGE_KIND}=="memory-stick", ENV{UDISKS_ICON_NAME}="media-flash-memory-stick", GOTO="gjallar_storage_icon_end"
    ENV{ID_DRIVE_FLASH_SD}=="1", ENV{UDISKS_ICON_NAME}="media-flash-sd-mmc", GOTO="gjallar_storage_icon_end"
    ENV{ID_DRIVE_FLASH_SD_COMBO}=="1", ENV{UDISKS_ICON_NAME}="media-flash-sd-mmc", GOTO="gjallar_storage_icon_end"
    ENV{ID_DRIVE_FLASH_MS}=="1", ENV{UDISKS_ICON_NAME}="media-flash-memory-stick", GOTO="gjallar_storage_icon_end"
    ENV{ID_DRIVE_FLASH_SM}=="1", ENV{UDISKS_ICON_NAME}="media-flash-smart-media", GOTO="gjallar_storage_icon_end"
    ENV{ID_DRIVE_FLASH_CF}=="1", ENV{UDISKS_ICON_NAME}="media-flash", GOTO="gjallar_storage_icon_end"
    ENV{GJALLAR_STORAGE_KIND}=="card", ENV{UDISKS_ICON_NAME}="media-flash", GOTO="gjallar_storage_icon_end"
    ATTRS{removable}=="1", ENV{UDISKS_ICON_NAME}="drive-removable-media-usb-pendrive", GOTO="gjallar_storage_icon_end"
    ATTRS{removable}=="0", ENV{UDISKS_ICON_NAME}="drive-harddisk"
    LABEL="gjallar_storage_icon_end"
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
  imports = [
    ../fixes/systemd-partition-reread
    ./usb-keyboard-gate.nix
  ];

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
      default =
        if config.oddc.device != null && config.oddc.catalog != null then oddcDevicesFile else null;
      defaultText = lib.literalMD "`oddc devices --json` for `oddc.device` from `oddc.catalog`";
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
    {
      services.udisks2.enable = true;
      services.udev.extraRules = storageIconRules;
    }
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
        # USBGuard sets new devices to start out deauthorized (AuthorizedDefault,
        # upstream default none). Restoring the kernel's value on stop let any
        # device plugged in while USBGuard was down, or after protection was
        # turned off, in without a decision (loophole 35). Upstream warns
        # about this and defaults to false. With false, turning protection
        # off needs a reboot before new USB devices work.
        restoreControllerDeviceState = false;
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

      # Read by `gjallarctl usb status` and `gjallarctl security notice`.
      environment.etc."gjallar/usb-keyboard-gate.json".text = builtins.toJSON keyboardGate;
      system.checks = lib.optional (cfg.oddcDevices != null) keyboardPortCheck;
      services.gjallar-usbtrust.earlyKeyboardGate = {
        enable = keyboardGate.earlyGate;
        devices = cfg.oddcDevices;
      };

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
