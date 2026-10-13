# system/fixes/avahi-daemon next to a stock avahi. The stock machine shows
# the bug; when it stops failing, the test says which part of the fix to drop.
{ pkgs, fixModule }:
let
  avahiNode = {
    services.avahi.enable = true;
    virtualisation.cores = 2;
  };
in
pkgs.testers.runNixOSTest {
  name = "avahi-daemon-fix";

  nodes.fixed = {
    imports = [
      fixModule
      avahiNode
    ];
  };
  nodes.stock = avahiNode;

  testScript = ''
    start_all()
    for m in (fixed, stock):
        m.wait_for_unit("avahi-daemon.service")


    def daemon_pid(m):
        return m.succeed("cat /run/avahi-daemon/pid").strip()


    def stopped(m):
        # Wait until systemd has run the stop commands, not only noticed
        # the death.
        m.wait_until_succeeds(
            "systemctl show -P ActiveState avahi-daemon.service | grep -Ex 'failed|inactive'"
        )


    def plant_stale_pid_file(m):
        # The pid of a process that has just exited, in a file owned by
        # avahi, like the one a killed daemon leaves.
        m.succeed(
            "systemctl stop avahi-daemon.service avahi-daemon.socket"
            " && dead=$(sh -c 'echo $$')"
            " && echo $dead > /run/avahi-daemon/pid"
            " && chown avahi:avahi /run/avahi-daemon/pid"
        )


    with subtest("a killed daemon leaves no pid file behind"):
        fixed.succeed(f"kill -KILL {daemon_pid(fixed)}")
        stopped(fixed)
        fixed.fail("test -e /run/avahi-daemon/pid")
        fixed.succeed("systemctl start avahi-daemon.service")

        stock.succeed(f"kill -KILL {daemon_pid(stock)}")
        stopped(stock)
        if stock.execute("test -e /run/avahi-daemon/pid")[0] != 0:
            raise Exception(
                "stock avahi no longer leaves a pid file when killed; check"
                " whether nixpkgs cleans it now and drop ExecStopPost in"
                " system/fixes/avahi-daemon"
            )

    with subtest("avahi starts over a stale pid file"):
        plant_stale_pid_file(fixed)
        fixed.succeed("systemctl start avahi-daemon.service")
        fixed.succeed("systemctl is-active avahi-daemon.service")

        plant_stale_pid_file(stock)
        if stock.execute("systemctl start avahi-daemon.service")[0] == 0:
            raise Exception(
                "stock avahi now starts over a stale pid file; drop"
                " ExecStartPre and ExecStopPost in system/fixes/avahi-daemon"
            )
        stock.succeed("journalctl -u avahi-daemon.service | grep -F 'Failed to create PID file'")
        stock.succeed("rm /run/avahi-daemon/pid && systemctl reset-failed avahi-daemon.service")
        stock.succeed("systemctl start avahi-daemon.service")

    # What the resolvconf hook does, as fast as the shell can, while avahi
    # restarts again and again. It runs as its own unit: a background job
    # dies with the test driver's shell.
    hammer = (
        "systemd-run --unit=sighup-hammer --quiet sh -c"
        " 'while :; do kill -HUP $(cat /var/run/avahi-daemon/pid 2>/dev/null) 2>/dev/null; done'"
    )
    rounds = 30


    def restarts_under_sighup(m):
        """Returns how many restarts ended with avahi gone."""
        m.succeed(hammer)
        deaths = 0
        for _ in range(rounds):
            # Many quick restarts trip systemd's start limit; that is not the
            # failure under test.
            m.succeed("systemctl reset-failed avahi-daemon.service avahi-daemon.socket")
            up = m.execute(
                "systemctl restart avahi-daemon.service"
                " && sleep 0.3"
                " && systemctl is-active avahi-daemon.service"
            )[0] == 0
            if not up:
                deaths += 1
                m.succeed(
                    "systemctl stop avahi-daemon.service;"
                    " rm -f /run/avahi-daemon/pid;"
                    " systemctl reset-failed avahi-daemon.service avahi-daemon.socket"
                )
        # A hammer that died early would make every round pass.
        m.succeed("systemctl is-active sighup-hammer.service")
        m.succeed("systemctl stop sighup-hammer.service")
        m.succeed("systemctl reset-failed avahi-daemon.service avahi-daemon.socket")
        m.succeed("systemctl start avahi-daemon.service")
        return deaths


    with subtest("SIGHUP from the resolvconf hook during start does not end avahi"):
        deaths = restarts_under_sighup(fixed)
        assert deaths == 0, f"avahi died in {deaths} of {rounds} restarts under SIGHUP"

    with subtest("SIGHUP still reloads a running avahi"):
        count = "journalctl -u avahi-daemon.service | grep -cF 'Got SIGHUP, reloading.' || true"
        before = int(fixed.succeed(count))
        pid = daemon_pid(fixed)
        fixed.succeed(f"kill -HUP {pid}")
        fixed.wait_until_succeeds(f"test $({count}) -gt {before}")
        assert daemon_pid(fixed) == pid, "avahi restarted instead of reloading"

    with subtest("stock avahi under the same SIGHUPs (record only)"):
        deaths = restarts_under_sighup(stock)
        # The gap is short, so stock often survives all rounds; the boot on
        # Jade's laptop that hit it is in Plane 181.
        print(f"stock avahi died in {deaths} of {rounds} restarts under SIGHUP")
  '';
}
