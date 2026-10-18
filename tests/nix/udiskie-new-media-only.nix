# pkgs/overlays/udiskie.nix next to a stock udiskie. Both automount, get a
# USB stick plugged in, and then have it written the way an image writer
# does it, well after the plug-in. The patched one leaves what the writer
# makes alone and still mounts at plug-in and on a new card. The stock one
# shows the bug; when it stops mounting the written stick, the test says to
# drop the patch.
{ pkgs, stockUdiskie }:
let
  node = udiskie: {
    services.udisks2.enable = true;
    environment.systemPackages = [ pkgs.dosfstools ];
    virtualisation.qemu.options = [ "-device qemu-xhci,id=xhci" ];
    systemd.services.udiskie = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${udiskie}/bin/udiskie --no-config --automount --no-notify --no-tray --no-file-manager --no-password-prompt";
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "udiskie-new-media-only";

  nodes.patched = node pkgs.udiskie;
  nodes.stock = node stockUdiskie;

  testScript = ''
    both = [patched, stock]


    def image(m):
        return f"{m.state_dir}/stick.img"


    def plug(m, name, path=None, removable=False):
        opts = f"if=none,id={name}" + (f",format=raw,file={path}" if path else "")
        m.send_monitor_command(f"drive_add 0 {opts}")
        m.send_monitor_command(
            f"device_add usb-storage,bus=xhci.0,drive={name},id={name}-dev,"
            f"removable={'on' if removable else 'off'}"
        )
        m.wait_for_file("/dev/sda")


    def unplug(m, name):
        m.execute("umount /dev/sda1")
        m.send_monitor_command(f"device_del {name}-dev")
        m.wait_until_succeeds("! test -e /dev/sda")


    def mounted(m):
        return m.execute("findmnt -n -S /dev/sda1")[0] == 0


    def wait_mounted(m, what):
        m.wait_until_succeeds("findmnt -n -S /dev/sda1", timeout=30)
        print(f"{m.name}: {what}: mounted")


    def settle(m):
        # the re-read after a write runs in a child of udevd, and udiskie
        # reacts to udisks after that
        m.succeed("sleep 3 && udevadm settle && sleep 3")


    start_all()
    for m in both:
        m.wait_for_unit("udiskie.service")
        with open(image(m), "wb") as f:
            f.truncate(64 << 20)
        # one vfat partition, the way an installer image brings its own
        m.succeed(
            "truncate -s 32M /tmp/dd.img",
            "printf 'label: gpt\\nstart=1M, size=16M\\n' | sfdisk --quiet /tmp/dd.img",
            "mkfs.vfat -n WRITTEN --offset 2048 /tmp/dd.img 16384",
        )
        plug(m, "stick", image(m))
        # the writer starts well after the plug-in, as a person would
        m.succeed("sleep 8")

    with subtest("a written image is not mounted"):
        for m in both:
            m.succeed("dd if=/tmp/dd.img of=/dev/sda bs=1M conv=fsync status=none")
            m.wait_for_file("/dev/sda1")
            settle(m)
        assert not mounted(patched), "patched udiskie mounted the image written to the stick"
        if not mounted(stock):
            raise Exception(
                "stock udiskie no longer mounts an image written to a plugged-in"
                " stick; drop pkgs/overlays/udiskie.nix"
            )

    with subtest("a filesystem made on the stick is not mounted"):
        for m in both:
            m.execute("umount /dev/sda1")
            m.succeed("wipefs -qa /dev/sda1")
            settle(m)
            m.succeed("mkfs.vfat -n MADE /dev/sda1")
            settle(m)
        assert not mounted(patched), "patched udiskie mounted a filesystem made on the stick"
        print(f"stock: mounts a filesystem made on the stick: {mounted(stock)}")

    with subtest("a stick plugged in is mounted"):
        for m in both:
            unplug(m, "stick")
            plug(m, "stick", image(m))
            wait_mounted(m, "plugged-in stick")

    with subtest("a card put into a reader is mounted"):
        for m in both:
            unplug(m, "stick")
            plug(m, "card", removable=True)
            # an empty reader that sat there a while, then gets a card
            m.succeed("sleep 8")
            m.send_monitor_command(f"change card {image(m)} raw")
            wait_mounted(m, "new card")
  '';
}
