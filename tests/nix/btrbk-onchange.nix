# pkgs/overlays/btrbk.nix next to a stock btrbk, both with snapshot_create
# onchange on a scratch btrfs. The patched one snapshots once, skips while
# nothing changes, snapshots a change that is not committed yet and makes a
# first snapshot of a subvolume put back from an old snapshot. The stock one
# shows the bug on linuxPackages_latest (7.x keeps the source generation when
# a snapshot is taken, 6.18 bumped it); when stock stops snapshotting the
# unchanged subvolume there, the test says to drop the patch.
{ pkgs, stockBtrbk }:
let
  node = {
    virtualisation.emptyDiskImages = [ 512 ];
    environment.systemPackages = [ pkgs.btrfs-progs ];
  };
in
pkgs.testers.runNixOSTest {
  name = "btrbk-onchange";

  # The stock bug shows only on the kernel GjallarOS ships; the patched
  # btrbk must work on the default one as well.
  nodes.latest = {
    imports = [ node ];
    boot.kernelPackages = pkgs.linuxPackages_latest;
  };
  nodes.lts = node;

  testScript = ''
    def setup(m, name):
        m.succeed(
            f"btrfs -q subvolume create /pool/{name}",
            f"mkdir -p /pool/{name}-snaps",
            f"""cat > /pool/{name}.conf <<CONF
    timestamp_format long-iso
    snapshot_create onchange
    snapshot_preserve_min all
    subvolume /pool/{name}
      snapshot_dir /pool/{name}-snaps
      snapshot_name {name}
    CONF""",
            f"echo one > /pool/{name}/file && sync",
        )


    def gens(m, name):
        gen = m.succeed(f"btrfs subvolume show /pool/{name} | sed -n 's/^\\s*Generation:\\s*//p'").strip()
        cgens = m.succeed(
            f"for s in /pool/{name}-snaps/*; do btrfs subvolume show $s | sed -n 's/^\\s*Gen at creation:\\s*//p'; done"
        ).split()
        print(f"{m.name} {name}: gen={gen} snapshot cgen={' '.join(cgens)}")


    def run(m, btrbk, name):
        m.succeed(f"{btrbk}/bin/btrbk -c /pool/{name}.conf snapshot")
        return int(m.succeed(f"ls /pool/{name}-snaps | wc -l"))


    def check(m):
        m.wait_for_unit("multi-user.target")
        # commit=300 keeps writes uncommitted until btrbk syncs them itself
        m.succeed(
            "mkfs.btrfs -q /dev/vdb",
            "mkdir -p /pool && mount -o commit=300 /dev/vdb /pool",
        )

        patched = "${pkgs.btrbk}"
        setup(m, "patched")
        assert run(m, patched, "patched") == 1, "no first snapshot"
        assert run(m, patched, "patched") == 1, "unchanged subvolume snapshotted again"
        assert run(m, patched, "patched") == 1, "unchanged subvolume snapshotted on the third run"

        m.succeed("echo two >> /pool/patched/file")
        assert run(m, patched, "patched") == 2, "uncommitted change skipped"
        assert run(m, patched, "patched") == 2, "snapshotted again after the change"
        gens(m, "patched")

        # Put the subvolume back from its first snapshot, as a rollback does.
        # The old snapshots come from the replaced subvolume, so the new one
        # gets its own first snapshot.
        first = m.succeed("ls /pool/patched-snaps | head -n 1").strip()
        m.succeed(
            "mv /pool/patched /pool/patched.old",
            f"btrfs -q subvolume snapshot /pool/patched-snaps/{first} /pool/patched",
            "sync",
        )
        assert run(m, patched, "patched") == 3, "rolled back subvolume got no snapshot"
        assert run(m, patched, "patched") == 3, "rolled back subvolume snapshotted again"

        setup(m, "stock")
        run(m, "${stockBtrbk}", "stock")
        count = run(m, "${stockBtrbk}", "stock")
        gens(m, "stock")
        return count


    start_all()
    check(lts)
    if check(latest) == 1:
        raise Exception("stock btrbk now skips unchanged subvolumes on linuxPackages_latest; drop pkgs/overlays/btrbk.nix")
  '';
}
