# nixpkgs and oddc: the source paths flake.lock pins (the flake's
# inputs.nixpkgs and inputs.oddc), never a channel or a branch head.
{
  nixpkgs,
  oddc,
}:
let
  # Any catalog model: the checks below hold for every machine.
  device = builtins.head ((import (oddc + "/lib")).mkRegistry (import (nixpkgs + "/lib"))).modelIds;

  evaluate =
    enabled: enforce: managed:
    let
      evaluated = import (nixpkgs + "/nixos/lib/eval-config.nix") {
        system = "x86_64-linux";
        specialArgs.settings = {
          username = "testuser";
          usbguardEnable = enabled;
          usbTrustEnforce = enforce;
          usbTrustTpmHandle = if enabled then "0x81000042" else "";
          endpointManagedDevice = managed;
        };
        modules = [
          (oddc + "/nixos/modules")
          ../system/services/removable-media.nix
          ({ lib, ... }: {
            options.home-manager.users = lib.mkOption {
              type = lib.types.attrsOf lib.types.raw;
              default = { };
            };
            config = {
              system.stateVersion = "26.05";
              oddc.device = device;
            };
          })
        ];
      };
      c = evaluated.config;
    in
    {
      backend = c.services.usbguard.enable;
      inherit (c.services.gjallar-usbtrust) enforce;
      policy = c.services.usbguard.implicitPolicyTarget;
      groups = c.services.usbguard.IPCAllowedGroups;
      users = c.services.usbguard.IPCAllowedUsers;
      rules = c.services.usbguard.rules;
      usbguardReadWritePaths =
        if enabled then c.systemd.services.usbguard.serviceConfig.ReadWritePaths else [ ];
      start = if enabled then c.systemd.services.usb-trust-broker.serviceConfig.ExecStart else null;
      desktopGroups =
        if enabled then (c.users.users.testuser or { extraGroups = [ ]; }).extraGroups else [ ];
      brokerGroup =
        if enabled then c.systemd.services.usb-trust-broker.serviceConfig.Group or null else null;
      runtimeMode =
        if enabled then c.systemd.services.usb-trust-broker.serviceConfig.RuntimeDirectoryMode else null;
      stateDirectory =
        if enabled then c.systemd.services.usb-trust-broker.serviceConfig.StateDirectory else null;
      # The broker needs /sys/bus/scsi at start (mount namespace and autoprobe).
      scsiAtBoot =
        !enabled
        || (
          builtins.elem "scsi_mod" c.boot.kernelModules
          && builtins.elem "systemd-modules-load.service" c.systemd.services.usb-trust-broker.after
        );
      # With blocking on, stage 1 turns SCSI autoprobe off before udevd and
      # binds what the media gate does not own (Plane 195).
      initrdGate =
        let
          unit = c.boot.initrd.systemd.services.gjallar-scsi-autoprobe-off or null;
        in
        unit != null
        && builtins.elem "systemd-udevd.service" unit.before
        && builtins.match ".*gjallar-scsi-bind-ungated.*" c.boot.initrd.services.udev.rules != null;
      startsWithBackend =
        !enabled || builtins.elem "usbguard.service" c.systemd.services.usb-trust-broker.wantedBy;
      review =
        if enabled then
          let
            hm = c.home-manager.users.testuser { pkgs = evaluated.pkgs; };
            services = hm.systemd.user.services;
          in
          if builtins.hasAttr "usb-device-review" services then
            services.usb-device-review.Service.ExecStart
          else
            null
        else
          null;
      usbAssertions = builtins.all (
        a: a.assertion || builtins.match "USB trust.*" a.message == null
      ) c.assertions;
    };
  dormant = evaluate false false false;
  audit = evaluate true false false;
  enforcing = evaluate true true false;
  managedAudit = evaluate true false true;
in
assert !dormant.backend;
assert audit.backend && !audit.enforce && audit.policy == "allow";
assert enforcing.policy == "block" && enforcing.rules == "";
assert enforcing.groups == [ ] && enforcing.users == [ "root" ];
assert !builtins.elem "usb-trust-access" enforcing.desktopGroups;
assert enforcing.brokerGroup == null && enforcing.runtimeMode == "0711";
assert enforcing.stateDirectory == "gjallar-usbtrust";
assert builtins.elem "-/sys" enforcing.usbguardReadWritePaths;
assert enforcing.usbAssertions;
assert audit.startsWithBackend && enforcing.startsWithBackend;
assert audit.scsiAtBoot && enforcing.scsiAtBoot;
assert enforcing.initrdGate && !audit.initrdGate && !dormant.initrdGate;
assert audit.review != null;
assert managedAudit.review == null;
{
  inherit
    dormant
    audit
    enforcing
    managedAudit
    ;
}
