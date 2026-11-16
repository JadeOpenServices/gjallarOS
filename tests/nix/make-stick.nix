# scripts/installation/make-stick.sh (Plane 350, CP-E) against USB sticks
# that are hot-plugged into the VM. The release comes from a local https
# server with a key made at build time; the images have the same partition
# layout as the stick ISO (MBR, slot 1 type 0xcd, slot 2 type 0xef) but
# are 8 MiB. Checks for loopholes L70, L71, L72 and L76 run the root code
# the maker passes to sudo, as root, with planted links, FIFOs, a mounted
# stick and streams that differ from what was signed. The rollback guard
# (Q228/Q231/Q232) gets releases below the floor, one that raises it, one
# signed 200 days ago and RELEASE files that do not match.
{
  pkgs,
  script,
  gjlogs,
}:
let
  fixtures =
    pkgs.runCommand "make-stick-fixtures"
      {
        nativeBuildInputs = with pkgs; [
          openssl
          util-linux
          gawk
        ];
      }
      ''
        set -eu
        mkdir -p $out
        cd $out
        openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out release.key 2>/dev/null
        openssl pkey -in release.key -pubout -out release.pub
        openssl req -x509 -newkey rsa:2048 -nodes -keyout tls.key -out tls.crt -days 36500 \
          -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 2>/dev/null

        # 16388 sectors: not a whole MiB, like the real ISO.
        image() { # file sfdisk-script
          openssl enc -aes-128-ctr -K 00 -iv 00 -nosalt -in /dev/zero 2>/dev/null | head -c 8390656 >"$1" || true
          [ "$(stat -c %s "$1")" = 8390656 ]
          printf '%s\n' "$2" | sfdisk -q --no-reread --no-tell-kernel "$1"
        }
        image mbr.iso 'label: dos
        start=64, size=12000, type=cd, bootable
        start=12064, size=4320, type=ef'
        image gpt.iso 'label: gpt
        start=2048, size=8000
        start=10048, size=4000'

        sign() { # dir
          openssl dgst -sha256 -sigopt rsa_padding_mode:pss -sigopt rsa_pss_saltlen:32 \
            -sign release.key -out "$1/SHA256SUMS.sig" "$1/SHA256SUMS"
        }
        # Releases are signed today; the test sets the VM clock to
        # tomorrow noon, so they never age into the 90-day question and
        # the TLS certificate is already valid.
        day=$(date -u +%F)
        date -u -d "$day + 1 day" '+%F 12:00' >vm-now
        release() { # dir image tag [date] [floor] [version in RELEASE]
          mkdir -p "$1"
          cp "$2" "$1/gjallarOS-installer-$3.iso"
          cp ${gjlogs}/gjlogs.img "$1/gjlogs.img"
          printf 'version=%s\ndate=%s\nfloor=%s\n' "''${6:-$3}" "''${4:-$day}" "''${5:-v2026.01.1}" >"$1/RELEASE"
          (cd "$1" && sha256sum "gjallarOS-installer-$3.iso" gjlogs.img RELEASE >SHA256SUMS)
          sign "$1"
        }
        r=www/releases
        release $r/latest/download mbr.iso v2026.01.1
        release $r/download/v2026.01.1 mbr.iso v2026.01.1
        release $r/download/v2026.01.2 gpt.iso v2026.01.2
        # Signed, but the image changed after signing.
        release $r/download/v2026.01.3 mbr.iso v2026.01.3
        printf x | dd of=$r/download/v2026.01.3/gjallarOS-installer-v2026.01.3.iso bs=1 seek=4096 conv=notrunc status=none
        # A real signature from the key, but over other bytes.
        release $r/download/v2026.01.4 mbr.iso v2026.01.4
        openssl dgst -sha256 -sigopt rsa_padding_mode:pss -sigopt rsa_pss_saltlen:32 \
          -sign release.key -out $r/download/v2026.01.4/SHA256SUMS.sig $r/download/v2026.01.4/gjlogs.img
        # A correctly signed older release served under a newer name.
        release $r/download/v2026.01.5 mbr.iso v2026.01.1
        # Rollback guard: below the maker's floor, a release that raises
        # the floor, one signed 200 days ago.
        release $r/download/v2026.01.0 mbr.iso v2026.01.0 "" v2026.01.0
        release $r/download/v2026.01.6 mbr.iso v2026.01.6 "" v2026.01.6
        release $r/download/v2026.01.7 mbr.iso v2026.01.7 "$(date -u -d "$day - 200 days" +%F)"
        # RELEASE changed after signing; signed but naming another
        # version; missing from SHA256SUMS.
        release $r/download/v2026.01.8 mbr.iso v2026.01.8
        printf 'version=v2026.01.9\ndate=%s\nfloor=v2026.01.1\n' "$day" >$r/download/v2026.01.8/RELEASE
        release $r/download/v2026.01.9 mbr.iso v2026.01.9 "" "" v2026.01.1
        release $r/download/v2026.01.10 mbr.iso v2026.01.10
        sed -i '/ RELEASE$/d' $r/download/v2026.01.10/SHA256SUMS
        sign $r/download/v2026.01.10

        cp ${script} make-stick.sh
        chmod u+w make-stick.sh
        substituteInPlace make-stick.sh \
          --replace-fail "@GJALLAR_RELEASE_KEY@" "$(sed '1d;$d' release.pub)" \
          --replace-fail "@GJALLAR_RELEASE_VERSION@" v2026.01.1 \
          --replace-fail "@GJALLAR_RELEASE_FLOOR@" v2026.01.1 \
          --replace-fail "https://github.com/JadeOpenServices/gjallarOS/releases" "https://127.0.0.1:8443/releases"
        awk "/local root_code='\$/ { f = 1; next } f && /^'\$/ { exit } f" make-stick.sh >root-code.sh
        grep -q '^echo "logs=\$logs"$' root-code.sh
        chmod -R a+rX .
      '';

  server = pkgs.writeText "release-server.py" ''
    import functools, http.server, ssl
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain("${fixtures}/tls.crt", "${fixtures}/tls.key")
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory="${fixtures}/www")
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 8443), handler)
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
    srv.serve_forever()
  '';
in
pkgs.testers.runNixOSTest {
  name = "make-stick";

  nodes.machine = {
    virtualisation.memorySize = 1024;
    virtualisation.qemu.options = [ "-device qemu-xhci,id=xhci" ];
    users.users.alice = {
      isNormalUser = true;
      extraGroups = [ "wheel" ];
    };
    security.sudo.wheelNeedsPassword = false;
    services.udisks2.enable = true;
    environment.systemPackages = with pkgs; [
      curl
      openssl
      dosfstools
    ];
    systemd.services.release-server = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 -I ${server}";
    };
  };

  testScript = ''
    import hashlib
    import shlex

    fx = "${fixtures}"
    iso_size = 8390656
    off = 9 * 1024 * 1024  # iso_size rounded up to a MiB
    img_size = 33554432
    maker_log = "/home/alice/.local/state/gjallaros/make-stick.log"
    seen_file = "/home/alice/.local/state/gjallaros/make-stick.seen"

    start_all()
    machine.wait_for_unit("release-server.service")
    machine.wait_for_open_port(8443)
    machine.wait_for_unit("multi-user.target")
    vm_now = open(f"{fx}/vm-now").read().strip()
    machine.succeed("timedatectl set-ntp false || true", f"date -u -s '{vm_now}'")

    def plug(name, size, serial, removable="on"):
        path = machine.state_dir / f"{name}.img"
        with open(path, "wb") as f:
            f.truncate(size)
        machine.send_monitor_command(f"drive_add 0 if=none,id={name},file={path},format=raw")
        machine.send_monitor_command(
            f"device_add usb-storage,bus=xhci.0,drive={name},id=u{name},serial={serial},removable={removable}"
        )
        link = f"/dev/disk/by-id/usb-QEMU_QEMU_HARDDISK_{serial}-0:0"
        machine.wait_until_succeeds(f"test -b {link}")
        machine.succeed("udevadm settle")
        return machine.succeed(f"readlink -f {link}").strip(), link.rsplit("/", 1)[1]

    def unplug(name, serial):
        machine.send_monitor_command(f"device_del u{name}")
        machine.wait_until_fails(f"ls /dev/disk/by-id/ | grep -q {serial}")

    def maker(args, answers, env=""):
        cmd = f"{env} CURL_CA_BUNDLE={fx}/tls.crt bash {fx}/make-stick.sh {args}"
        run = f"printf {shlex.quote(answers)} | script -qec {shlex.quote(cmd)} /dev/null"
        machine.succeed(f"rm -f {maker_log}")
        return machine.execute(f"su - alice -c {shlex.quote(run)}")

    def anchors():
        return machine.succeed(f"cat {maker_log}")

    def head_sha(dev, n=16 * 1024 * 1024):
        return machine.succeed(f"head -c {n} {dev} | sha256sum").split()[0]

    def no_download_left():
        machine.fail("ls -d /home/alice/.cache/gjallar-make-stick.*")

    iso_sha = hashlib.sha256(open(f"{fx}/mbr.iso", "rb").read()).hexdigest()

    with subtest("only sticks big enough are offered"):
        dev, _ = plug("small", 4 * 1024 * 1024, "GJTESTC3")
        rc, out = maker("--release v2026.01.1", "q\n")
        assert rc == 130, (rc, out)
        assert "too small" in out, out
        assert "GJA-3099 maker.run.finished code=130" in anchors()
        no_download_left()
        unplug("small", "GJTESTC3")

    dev, by_id = plug("big", 64 * 1024 * 1024, "GJTESTA1")

    with subtest("L76: stable release written, read back, GJLOGS added"):
        rc, out = maker("", "\n1\nERASE\n")
        assert rc == 0, (rc, out)
        assert "GJTESTA1" in out, "the list shows the serial"
        # Equal to the ISO except MBR slot 3 (bytes 478-493), which the
        # maker fills; sfdisk below checks that entry.
        machine.succeed(f"cmp -n 478 {dev} {fx}/mbr.iso")
        machine.succeed(f"cmp -i 494 -n {iso_size - 494} {dev} {fx}/mbr.iso")
        machine.succeed(f"cmp -i 0:{off} -n {img_size} ${gjlogs}/gjlogs.img {dev}")
        table = " ".join(machine.succeed(f"sfdisk -d {dev}").split())
        assert f"{dev}3 : start= {off // 512}, size= {img_size // 512}, type=e" in table, table
        machine.wait_until_succeeds(f"blkid -s LABEL -o value {dev}3 | grep -qx GJLOGS")
        a = anchors()
        for want in [
            "GJA-3001 maker.run.started os=linux",
            "GJA-3010 maker.download.finished",
            "GJA-3011 maker.signature.verified",
            "GJA-3020 maker.disk.chosen disks=1",
            "GJA-3030 maker.write.finished",
            "GJA-3031 maker.readback.verified",
            "GJA-3040 maker.logs.created",
            "GJA-3099 maker.run.finished code=0",
        ]:
            assert want in a, (want, a)
        assert "GJTESTA1" not in a, "serial in the log"
        no_download_left()

    good = head_sha(dev, off + img_size)

    with subtest("bad releases change nothing"):
        for tag in ["v2026.01.3", "v2026.01.4", "v2026.01.5", "v2026.01.8", "v2026.01.9", "v2026.01.10"]:
            rc, out = maker(f"--release {tag}", "")
            assert rc == 1, (tag, rc, out)
            assert "GJA-3012 maker.signature.bad" in anchors(), tag
            assert head_sha(dev, off + img_size) == good, tag
            no_download_left()

    with subtest("a mounted stick is refused"):
        machine.succeed(f"mkdir -p /mnt/l && mount {dev}3 /mnt/l")
        rc, out = maker("--release v2026.01.1", "1\nERASE\n")
        assert rc == 1 and "in use" in out, (rc, out)
        machine.succeed("umount /mnt/l")
        assert head_sha(dev, off + img_size) == good

    # The root part, called the way the maker calls it.
    serial_hex = "GJTESTA1".encode().hex()
    size = 64 * 1024 * 1024

    def root(args, stream):
        return machine.execute(
            f"{stream} | bash --noprofile --norc -c \"$(cat {fx}/root-code.sh)\" make-stick-root {args}"
        )[0]

    good_args = f"{by_id} {serial_hex} {size} {iso_size} {iso_sha} " + hashlib.sha256(
        open("${gjlogs}/gjlogs.img", "rb").read()
    ).hexdigest()
    stream = f"cat {fx}/mbr.iso ${gjlogs}/gjlogs.img"

    with subtest("L70: by-id names that are not the USB disk"):
        machine.succeed(
            "truncate -s 64M /tmp/loopfile",
            "losetup /dev/loop7 /tmp/loopfile",
            "mkfifo /tmp/fifo",
            "touch /tmp/file",
            "ln -s ../../loop7 /dev/disk/by-id/usb-planted-loop",
            "ln -s /tmp/fifo /dev/disk/by-id/usb-planted-fifo",
            "ln -s /tmp/file /dev/disk/by-id/usb-planted-file",
            f"ln -s ../../{dev.rsplit('/', 1)[1]}3 /dev/disk/by-id/usb-planted-part",
        )
        for name in ["planted-loop", "planted-fifo", "planted-file", "planted-part"]:
            rc = root(f"usb-{name} {good_args.split(' ', 1)[1]}", stream)
            assert rc == 11, (name, rc)
        machine.succeed("test ! -s /tmp/file", "cmp -n 1048576 /tmp/loopfile /dev/zero")
        rest = good_args.split(" ")
        assert root(" ".join([rest[0], "47", *rest[2:]]), stream) == 11, "serial"
        assert root(" ".join([*rest[:2], str(size + 512), *rest[3:]]), stream) == 11, "size"
        assert root(" ".join(["usb-../../loop7", *rest[1:]]), stream) == 10, "path in id"
        assert head_sha(dev, off + img_size) == good

    with subtest("L71: a stream that is not what was signed wipes the start"):
        rc = root(good_args, f"{{ {stream}; printf x; }}")
        assert rc == 20, rc
        machine.succeed(f"cmp -n 1048576 {dev} /dev/zero")
        rc = root(good_args, stream)
        assert rc == 0, rc
        assert head_sha(dev, off + img_size) == good
        flipped = f"{{ head -c 4096 {fx}/mbr.iso; printf x; tail -c +4098 {fx}/mbr.iso; cat ${gjlogs}/gjlogs.img; }}"
        rc = root(good_args, flipped)
        assert rc == 21, rc
        machine.succeed(f"cmp -n 1048576 {dev} /dev/zero")

    with subtest("L72: BASH_ENV of the user does not reach root"):
        machine.succeed(
            "printf 'touch /tmp/bash-env-ran-$(id -u)\\n' > /home/alice/evil.sh",
            "chown alice /home/alice/evil.sh",
        )
        rc, out = maker("--release v2026.01.1", "1\nERASE\n", env="BASH_ENV=/home/alice/evil.sh")
        assert rc == 0, (rc, out)
        machine.succeed("test -e /tmp/bash-env-ran-1000")
        machine.fail("test -e /tmp/bash-env-ran-0")
        assert head_sha(dev, off + img_size) == good

    with subtest("the list shows partitions and labels"):
        machine.wait_until_succeeds(f"lsblk -no LABEL {dev} | grep -qx GJLOGS")
        rc, out = maker("--release v2026.01.1", "1\n\n")
        assert rc == 130, (rc, out)
        assert 'label "GJLOGS"' in out, out
        assert "external" not in out, "a removable stick is not an external drive"
        assert head_sha(dev, off + img_size) == good

    with subtest("a GPT image gets no GJLOGS slot"):
        rc, out = maker("--release v2026.01.2", "1\nERASE\n")
        assert rc == 0, (rc, out)
        machine.succeed(f"cmp -n {iso_size} {dev} {fx}/gpt.iso")
        assert "GJA-3041 maker.logs.skipped reason=layout" in anchors()
        assert "GJLOGS" not in machine.succeed(f"blkid -p {dev} || true")

    unplug("big", "GJTESTA1")

    with subtest("Q228: an external drive needs EXTERNAL typed"):
        dev, _ = plug("ext", 64 * 1024 * 1024, "GJTESTD4", removable="off")
        machine.succeed(f"printf 'backup data' | dd of={dev} bs=1 seek=1048576 conv=notrunc,fsync status=none")
        before = head_sha(dev)
        for answers in ["1\nERASE\n", "1\nERASE\n\n", "1\nERASE\nexternal\n"]:
            rc, out = maker("--release v2026.01.1", answers)
            assert rc == 130, (answers, rc, out)
            assert "Looks like an external drive: not marked removable" in out, out
            assert "Nothing was written." in out, out
            assert head_sha(dev) == before, answers
        rc, out = maker("--release v2026.01.1", "1\nERASE\nEXTERNAL\n")
        assert rc == 0, (rc, out)
        assert "GJA-3023 maker.disk.external" in anchors()
        machine.succeed(f"cmp -n 478 {dev} {fx}/mbr.iso")
        unplug("ext", "GJTESTD4")
        # Marked removable but bigger than sticks usually are (sparse file).
        dev, _ = plug("big", 140 * 1000**3, "GJTESTD5")
        before = head_sha(dev)
        rc, out = maker("--release v2026.01.1", "1\nERASE\n\n")
        assert rc == 130, (rc, out)
        assert "Looks like an external drive: bigger than 130.0 GB" in out, out
        assert "not marked removable" not in out, out
        assert head_sha(dev) == before
        unplug("big", "GJTESTD5")

    with subtest("a stick with no room after the image"):
        dev, _ = plug("mid", 9 * 1024 * 1024, "GJTESTB2")
        rc, out = maker("--release v2026.01.1", "1\nERASE\n")
        assert rc == 0, (rc, out)
        machine.succeed(f"cmp -n {iso_size} {dev} {fx}/mbr.iso")
        assert "GJA-3041 maker.logs.skipped reason=too-small" in anchors()
        assert "start=" + str(off // 512) not in machine.succeed(f"sfdisk -d {dev}")

    def seen():
        return machine.succeed(f"cat {seen_file}")

    with subtest("Q231: rollback and floor"):
        before = head_sha(dev)
        assert seen() == "version=v2026.01.2\nfloor=v2026.01.1\n", seen()
        # The server offers v2026.01.1 as newest, this computer used .2.
        rc, out = maker("", "\n1\nERASE\n")
        assert rc == 1, (rc, out)
        assert "already used v2026.01.2" in out, out
        assert "GJA-3013 maker.release.refused reason=rollback" in anchors()
        no_download_left()
        # Picked by name: allowed with a note; the record never goes down.
        rc, out = maker("--release v2026.01.1", "1\n\n")
        assert rc == 130, (rc, out)
        assert "older than v2026.01.2" in out, out
        assert "GJA-3014 maker.release.older reason=seen" in anchors()
        assert seen() == "version=v2026.01.2\nfloor=v2026.01.1\n", seen()
        # Below the floor built into the maker.
        rc, out = maker("--release v2026.01.0", "")
        assert rc == 1, (rc, out)
        assert "GJA-3013 maker.release.refused reason=floor" in anchors()
        no_download_left()
        # A newer release raises the floor; older ones are refused after.
        rc, out = maker("--release v2026.01.6", "1\n\n")
        assert rc == 130, (rc, out)
        assert seen() == "version=v2026.01.6\nfloor=v2026.01.6\n", seen()
        rc, out = maker("--release v2026.01.2", "")
        assert rc == 1, (rc, out)
        assert "GJA-3013 maker.release.refused reason=floor" in anchors()
        # A broken record or a FIFO is skipped, not trusted or waited on,
        # and replaced by a good one.
        for plant in ["printf junk", "mkfifo"]:
            arg = f"> {seen_file}" if plant == "printf junk" else seen_file
            machine.succeed(f"rm -f {seen_file}", f"su alice -c {shlex.quote(plant + ' ' + arg)}")
            rc, out = maker("--release v2026.01.2", "1\n\n")
            assert rc == 130, (plant, rc, out)
            assert "is not in the expected format" in out, out
            assert seen() == "version=v2026.01.2\nfloor=v2026.01.1\n", seen()
        assert head_sha(dev) == before

    with subtest("Q232: a release signed more than 90 days ago needs OLD"):
        before = head_sha(dev)
        for answers in ["\n", "old\n1\nERASE\n"]:
            rc, out = maker("--release v2026.01.7", answers)
            assert rc == 130, (answers, rc, out)
            assert "more than 90 days ago" in out, out
            assert "Nothing was written." in out, out
            assert head_sha(dev) == before, answers
        rc, out = maker("--release v2026.01.7", "OLD\n1\nERASE\n")
        assert rc == 0, (rc, out)
        assert "GJA-3014 maker.release.older reason=date" in anchors()
        machine.succeed(f"cmp -n {iso_size} {dev} {fx}/mbr.iso")
  '';
}
