# system/services/usb-keyboard-gate.nix in a VM with EMULATED QEMU USB
# devices. The devices file names the QEMU keyboard (0627:0001) on root port
# 1 of the QEMU xHCI controller (1b36:000d). Stage 1 must let in only that
# keyboard; the mouse and second keyboard with the same ID on other ports,
# the smartcard reader, and the tablet on root port 1 of the default UHCI
# controller stay closed until USBGuard decides in stage 2. With two xHCI
# controllers the ID no longer names one controller, and nothing gets in.
{ pkgs, gateModule }:
let
  # EMULATED: the shape `oddc devices --json` gives, with QEMU's IDs.
  devices = pkgs.writeText "emulated-oddc-devices.json" (
    builtins.toJSON {
      devices = [
        {
          path = "hardware.input.keyboard.internal";
          name = "Keyboard";
          deviceId = "0627:0001";
          attachment = "detachable";
          usbPort = {
            controller = "1b36:000d";
            hub = "usb2";
            port = 1;
          };
        }
      ];
    }
  );

  gatedNode = {
    imports = [ gateModule ];
    boot.initrd.systemd.enable = true;
    services.gjallar-usbtrust.earlyKeyboardGate = {
      enable = true;
      inherit devices;
    };
    # Started by hand, after the stage 1 state is checked. Unknown devices
    # are let in, the way an unarmed broker leaves them.
    services.usbguard = {
      enable = true;
      rules = "";
      implicitPolicyTarget = "allow";
      presentDevicePolicy = "apply-policy";
    };
    systemd.services.usbguard.wantedBy = pkgs.lib.mkForce [ ];
  };

  ports = [
    "-device usb-kbd,bus=xhci.0,port=1,id=kbd"
    "-device usb-mouse,bus=xhci.0,port=2"
    "-device usb-kbd,bus=xhci.0,port=3"
    "-device usb-ccid,bus=xhci.0,port=4"
  ];
in
pkgs.testers.runNixOSTest {
  name = "usb-keyboard-gate";

  nodes.gated = {
    imports = [ gatedNode ];
    virtualisation.qemu.options = [ "-device qemu-xhci,id=xhci" ] ++ ports;
  };
  nodes.shared = {
    imports = [ gatedNode ];
    virtualisation.qemu.options = [
      "-device qemu-xhci,id=xhci"
      "-device qemu-xhci,id=xhci2"
    ]
    ++ ports;
  };

  testScript = ''
    # The USB 2 root hub of the first QEMU xHCI controller, by bus number.
    find_bus = (
        "for h in /sys/bus/usb/devices/usb*; do "
        "p=$(readlink -f $h/..); "
        "[ \"$(cat $p/vendor):$(cat $p/device)\" = 0x1b36:0x000d ] || continue; "
        "case \"$(cat $h/version)\" in *2.*) cat $h/busnum;; esac; "
        "done | sort -n | head -n1"
    )


    def bus(m):
        return int(m.succeed(find_bus))


    def authorized(m, name):
        return m.succeed(f"cat /sys/bus/usb/devices/{name}/authorized").strip()


    def uhci_tablet(m):
        # The tablet qemu-vm puts on the default UHCI controller.
        path = m.succeed("grep -lx 'QEMU USB Tablet' /sys/bus/usb/devices/*/product").strip()
        return path.split("/")[-2]


    start_all()

    with subtest("stage 1 lets in only the keyboard on its port"):
        gated.wait_for_unit("multi-user.target")
        b = bus(gated)
        assert authorized(gated, f"{b}-1") == "1"
        for port in (2, 3, 4):
            assert authorized(gated, f"{b}-{port}") == "0", port
        gated.succeed(f"test -e /sys/bus/usb/devices/{b}-1:1.0/driver")
        gated.succeed(f"dmesg | grep -F 'gjallar-usb-keyboard-gate: {b}-1: built-in keyboard let in'")
        gated.succeed(f"dmesg | grep -F 'gjallar-usb-keyboard-gate: {b}-3: not on root port 1, stays closed'")
        tablet = uhci_tablet(gated)
        assert authorized(gated, tablet) == "0"
        gated.succeed(f"dmesg | grep -F 'gjallar-usb-keyboard-gate: {tablet}: not on controller 1b36:000d, stays closed'")
        gated.succeed("grep -F usbcore.authorized_default=0 /proc/cmdline")

    with subtest("the allow rule is not in stage 2"):
        gated.fail("grep -rqF gjallar-usb-keyboard-gate /etc/udev/rules.d")
        gated.send_monitor_command("device_del kbd")
        gated.wait_until_fails(f"test -e /sys/bus/usb/devices/{b}-1")
        gated.send_monitor_command("device_add usb-kbd,bus=xhci.0,port=1,id=kbd")
        gated.wait_until_succeeds(f"test -e /sys/bus/usb/devices/{b}-1/authorized")
        assert authorized(gated, f"{b}-1") == "0"

    with subtest("USBGuard decides every device after stage 1"):
        gated.succeed("systemctl start usbguard")
        for port in (1, 2, 3, 4):
            gated.wait_until_succeeds(f"grep -qx 1 /sys/bus/usb/devices/{b}-{port}/authorized")
        gated.wait_until_succeeds(f"grep -qx 1 /sys/bus/usb/devices/{tablet}/authorized")

    with subtest("a controller ID that is not unique lets nothing in"):
        shared.wait_for_unit("multi-user.target")
        for b in (int(x) for x in shared.succeed(
            "for h in /sys/bus/usb/devices/usb*; do "
            "p=$(readlink -f $h/..); "
            "[ \"$(cat $p/vendor):$(cat $p/device)\" = 0x1b36:0x000d ] && cat $h/busnum; "
            "done"
        ).split()):
            shared.succeed(f"for d in /sys/bus/usb/devices/{b}-*; do [ -e \"$d\" ] || continue; case $d in *:*) continue;; esac; grep -qx 0 $d/authorized; done")
        shared.succeed("dmesg | grep -F '2 controllers with ID 1b36:000d, stays closed'")
  '';
}
