# system/fixes/systemd-partition-reread next to a stock systemd. Both write
# one GPT image over another with dd, the way an image ends up on a stick.
# The stock machine shows the bug; when it stops failing, the test says to
# drop the fix.
{ pkgs, fixModule }:
let
  diskNode = {
    # /dev/vdb, watched by udev like a USB stick
    virtualisation.emptyDiskImages = [ 64 ];
  };
in
pkgs.testers.runNixOSTest {
  name = "systemd-partition-reread-fix";

  nodes.fixed = {
    imports = [
      fixModule
      diskNode
    ];
  };
  nodes.stock = diskNode;

  testScript = ''
    # The first image puts partition 1 behind partition 2. The second moves
    # both and adds a third: its partition 1 overlaps the old partition 2,
    # and its partition 3 overlaps the old partition 1. Starts in sectors.
    old_table = (
        "label: gpt\n"
        "label-id: 11111111-1111-4111-8111-111111111111\n"
        "start=30M, size=20M\n"
        "start=2M, size=5M\n"
    )
    new_table = (
        "label: gpt\n"
        "label-id: 22222222-2222-4222-8222-222222222222\n"
        "start=1M, size=16M\n"
        "start=20M, size=10M\n"
        "start=40M, size=10M\n"
    )
    old_starts = {1: 61440, 2: 4096}
    new_starts = {1: 2048, 2: 40960, 3: 81920}
    new_uuid = "22222222-2222-4222-8222-222222222222"


    def make_image(m, path, table):
        m.succeed(f"truncate -s 64M {path}")
        m.succeed(f"printf '{table}' | sfdisk --quiet {path}")


    def write(m, path):
        # Closing the written disk makes udev re-read its partition table.
        m.succeed(f"dd if={path} of=/dev/vdb bs=1M conv=fsync status=none")


    def starts(m):
        found = {}
        for n in range(1, 9):
            code, out = m.execute(f"cat /sys/class/block/vdb{n}/start")
            if code == 0:
                found[n] = int(out)
        return found


    def disk_uuid(m):
        return m.succeed(
            "udevadm info --query=property --property=ID_PART_TABLE_UUID --value /dev/vdb"
        ).strip()


    def wait_quiet(m):
        # The re-read runs in a child of udevd after the write, so settle
        # alone can return before its events are queued.
        m.succeed("sleep 3 && udevadm settle")


    start_all()
    for m in (fixed, stock):
        m.wait_for_unit("systemd-udevd.service")
        make_image(m, "/tmp/old.img", old_table)
        make_image(m, "/tmp/new.img", new_table)
        write(m, "/tmp/old.img")
        wait_quiet(m)
        found = starts(m)
        assert found == old_starts, f"{m.name}: after the first image: {found}"

    with subtest("every new partition appears at its new start"):
        write(fixed, "/tmp/new.img")
        fixed.wait_until_succeeds(
            "test $(cat /sys/class/block/vdb1/start) = 2048"
            " && test $(cat /sys/class/block/vdb2/start) = 40960"
            " && test $(cat /sys/class/block/vdb3/start) = 81920",
            timeout=30,
        )
        wait_quiet(fixed)
        found = starts(fixed)
        assert found == new_starts, f"partitions after the second image: {found}"

    with subtest("udev sees the new disk, not the old one"):
        fixed.wait_until_succeeds(
            "udevadm info --query=property --property=ID_PART_TABLE_UUID --value /dev/vdb"
            f" | grep -qx {new_uuid}",
            timeout=30,
        )
        for n in new_starts:
            fixed.succeed(f"udevadm info /dev/vdb{n} | grep -q 'E: ID_PART_ENTRY_TYPE='")

    with subtest("stock systemd under the same write"):
        write(stock, "/tmp/new.img")
        wait_quiet(stock)
        found = starts(stock)
        uuid = disk_uuid(stock)
        print(f"stock: partitions {found}, disk table uuid {uuid!r}")
        if found == new_starts and uuid == new_uuid:
            raise Exception(
                "stock systemd now shows every new partition and the new disk"
                " after a dd; drop system/fixes/systemd-partition-reread"
            )
  '';
}
