# system/services/foreign-media-readonly.nix (Plane 286): FAT, exFAT, NTFS
# and HFS+ disks open read-only through udisks, a client that asks for rw
# still gets a writable mount, and ext4 is left as it was. The check also
# compares each line of the module with the built-in udisks defaults, so a
# udisks update that changes them fails here instead of silently dropping
# options.
{ pkgs, module }:
pkgs.testers.runNixOSTest {
  name = "foreign-media-readonly";

  nodes.machine = {
    imports = [ module ];
    services.udisks2.enable = true;
    boot.supportedFilesystems = [ "ntfs" "exfat" "vfat" "hfsplus" ];
    environment.systemPackages = with pkgs; [
      dosfstools
      exfatprogs
      ntfs3g
      hfsprogs
      e2fsprogs
    ];
  };

  testScript = ''
    import configparser
    import re

    start_all()
    machine.wait_for_unit("multi-user.target")

    with subtest("each line is the udisks built-in line plus ro"):
        builtin = configparser.ConfigParser(interpolation=None, delimiters=("=",))
        builtin.read("${pkgs.udisks2.src}/data/builtin_mount_options.conf")
        ours = configparser.ConfigParser(interpolation=None, delimiters=("=",))
        ours.read_string(machine.succeed("cat /etc/udisks2/mount_options.conf"))
        keys = list(ours["defaults"])
        assert set(keys) == {
            "vfat_defaults",
            "exfat_defaults",
            "ntfs:ntfs_defaults",
            "ntfs:ntfs3_defaults",
            "hfsplus_defaults",
        }, keys
        for key in keys:
            want = builtin["defaults"][key] + ",ro"
            assert ours["defaults"][key] == want, (key, ours["defaults"][key], want)

    mkfs = {
        "vfat": "mkfs.vfat -n EXAMPLE",
        "exfat": "mkfs.exfat -L EXAMPLE",
        "ntfs": "mkntfs -Q -F -L EXAMPLE",
        "hfsplus": "mkfs.hfsplus -v EXAMPLE",
        "ext4": "mkfs.ext4 -q -L EXAMPLE",
    }

    def find(pattern, text):
        found = re.search(pattern, text)
        assert found, (pattern, text)
        return found.group(1)

    def setup(fs):
        img = f"/tmp/{fs}.img"
        machine.succeed(f"truncate -s 64M {img}", f"{mkfs[fs]} {img}")
        out = machine.succeed(f"udisksctl loop-setup --no-user-interaction -f {img}")
        dev = find(r"(/dev/loop\d+)", out)
        machine.wait_until_succeeds(f"udisksctl info -b {dev} | grep -q 'IdType:.*{fs}'")
        return dev

    def mount(dev, *extra):
        out = machine.succeed(
            f"udisksctl mount --no-user-interaction -b {dev} " + " ".join(extra)
        )
        return find(r" at (\S+)", out).rstrip(".")

    def options(dev):
        return machine.succeed(f"findmnt -n -o OPTIONS -S {dev}").strip().split(",")

    def umount(dev):
        machine.succeed(f"udisksctl unmount --no-user-interaction -b {dev}")

    for fs in ["vfat", "exfat", "ntfs", "hfsplus"]:
        with subtest(f"{fs} opens read-only, rw on request"):
            dev = setup(fs)
            where = mount(dev)
            opts = options(dev)
            assert "ro" in opts, (fs, opts)
            machine.fail(f"touch {where}/written")
            umount(dev)
            where = mount(dev, "-o", "rw")
            opts = options(dev)
            assert "rw" in opts, (fs, opts)
            machine.succeed(f"touch {where}/written")
            umount(dev)

    with subtest("ext4 keeps its writable default"):
        dev = setup("ext4")
        where = mount(dev)
        assert "rw" in options(dev), options(dev)
        umount(dev)
  '';
}
