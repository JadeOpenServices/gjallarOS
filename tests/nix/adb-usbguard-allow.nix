# The libusb that android-tools bundles, patched by
# pkgs/overlays/android-tools.nix, next to the stock one. A small listener
# built from each copy watches a USB stick that USBGuard blocks at plug-in
# and that is then allowed, the way a phone reaches adb. While blocked the
# stick has no active configuration, so adb skips it. Allowing it sends no
# uevent for the device; the kernel only adds its interfaces. The patched
# copy watches those and reports the stick again; the stock copy stays
# silent until the stick is pulled. When the stock copy reports it too, the
# test says to drop the patch.
{ pkgs, patchedTools, stockTools }:
let
  listener =
    tools:
    pkgs.stdenv.mkDerivation {
      name = "libusb-hotplug-listener-${tools.version}";
      inherit (tools) src patches;
      nativeBuildInputs = [ pkgs.pkg-config ];
      buildInputs = [ pkgs.udev ];
      dontConfigure = true;
      # The source list and defines that vendor/CMakeLists.libusb.txt uses
      # on Linux with udev.
      buildPhase = ''
        runHook preBuild
        L=vendor/libusb/libusb
        $CC -O2 -Wall -Werror -DHAVE_LIBUDEV -I$L -I$L/os -Ivendor/libusb/linux \
          ${./adb-usbguard-allow.c} \
          $L/core.c $L/descriptor.c $L/hotplug.c $L/io.c $L/sync.c $L/strerror.c \
          $L/os/events_posix.c $L/os/linux_usbfs.c $L/os/threads_posix.c $L/os/linux_udev.c \
          $(pkg-config --libs libudev) -pthread -o listener
        runHook postBuild
      '';
      installPhase = ''
        install -Dm755 listener $out/bin/libusb-hotplug-listener
      '';
    };

  node = tools: {
    virtualisation.qemu.options = [ "-device qemu-xhci,id=xhci" ];
    services.usbguard = {
      enable = true;
      presentDevicePolicy = "allow";
      insertedDevicePolicy = "apply-policy";
      implicitPolicyTarget = "block";
      rules = "";
    };
    environment.systemPackages = [ (listener tools) ];
  };
in
pkgs.testers.runNixOSTest {
  name = "adb-usbguard-allow";

  nodes.patched = node patchedTools;
  nodes.stock = node stockTools;

  testScript = ''
    both = [patched, stock]


    def events(m):
        return m.succeed("cat /tmp/listener.log").split("\n")[1:-1]


    start_all()
    for m in both:
        m.wait_for_unit("usbguard.service")
        m.succeed(
            "systemd-run --unit=uevents -p StandardOutput=file:/tmp/uevents.log"
            " udevadm monitor --kernel --udev --subsystem-match=usb",
            "systemd-run --unit=listener -p StandardOutput=file:/tmp/listener.log"
            " -p StandardError=journal libusb-hotplug-listener",
        )
        m.wait_until_succeeds("grep -qx listening /tmp/listener.log")
        with open(f"{m.state_dir}/stick.img", "wb") as f:
            f.truncate(16 << 20)
        m.send_monitor_command(f"drive_add 0 if=none,id=stick,format=raw,file={m.state_dir}/stick.img")
        m.send_monitor_command("device_add usb-storage,bus=xhci.0,drive=stick,id=stick-dev")

    with subtest("a blocked device arrives without an active configuration"):
        for m in both:
            m.wait_until_succeeds("grep -q '^arrived 46f4:0001' /tmp/listener.log")
            m.succeed("usbguard list-devices --blocked | grep -q 'id 46f4:0001'")
            assert events(m) == ["arrived 46f4:0001 config missing"], events(m)

    with subtest("allowing it reports it again with its configuration"):
        for m in both:
            dev = m.succeed("usbguard list-devices --blocked | grep 'id 46f4:0001' | cut -d: -f1").strip()
            m.succeed(f"usbguard allow-device {dev}")
            m.wait_for_file("/dev/sda")
            m.succeed("udevadm settle")
            print(m.succeed("cat /tmp/uevents.log"))
        patched.wait_until_succeeds("grep -qx 'arrived 46f4:0001 config ok' /tmp/listener.log")

    with subtest("interfaces showing up on an authorized device are not reported"):
        for m in both:
            m.succeed(
                "udevadm trigger --action=add --settle --subsystem-match=usb --property-match=DEVTYPE=usb_interface"
            )
            m.succeed("test -e /dev/sda")
        assert events(patched) == [
            "arrived 46f4:0001 config missing",
            "left 46f4:0001",
            "arrived 46f4:0001 config ok",
        ], events(patched)

    with subtest("pulling the stick ends the story"):
        for m, lefts in [(patched, 2), (stock, 1)]:
            m.send_monitor_command("device_del stick-dev")
            m.wait_until_succeeds(f"[ $(grep -cx 'left 46f4:0001' /tmp/listener.log) -ge {lefts} ]")
            m.wait_until_succeeds("! test -e /dev/sda")
        # Events come in order, so everything before the last "left" is in.
        assert events(patched) == [
            "arrived 46f4:0001 config missing",
            "left 46f4:0001",
            "arrived 46f4:0001 config ok",
            "left 46f4:0001",
        ], events(patched)
        if events(stock) != ["arrived 46f4:0001 config missing", "left 46f4:0001"]:
            raise Exception(
                f"stock libusb now reports an allowed device again ({events(stock)});"
                " drop pkgs/overlays/android-tools.nix"
            )
  '';
}
