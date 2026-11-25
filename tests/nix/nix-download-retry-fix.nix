# system/fixes/nix-download-retry next to stock Nix. A local cache cuts every
# NAR reply after 1 MiB, so a 12 MiB path needs twelve connections that each
# move the download forward. Stock Nix gives up after download-attempts (5);
# the fixed one finishes. A cache that sends nothing still fails after five
# tries. When stock Nix stops failing, the test says to drop the fix.
{ pkgs, fixModule }:
let
  server = ./nix-download-retry-cache.py;
  cache = name: port: cut: {
    description = "test binary cache that cuts NAR replies (${name})";
    serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 -I ${server} /srv/cache ${toString port} ${toString cut}";
  };
in
pkgs.testers.runNixOSTest {
  name = "nix-download-retry-fix";

  nodes.machine = {
    imports = [ fixModule ];
    nix.settings.experimental-features = [ "nix-command" ];
    virtualisation.cores = 2;
    virtualisation.memorySize = 1024;
    systemd.services.cut-cache = cache "1 MiB per connection" 8080 1048576;
    systemd.services.dead-cache = cache "nothing" 8081 0;
  };

  testScript =
    { nodes, ... }:
    let
      fixed = "${nodes.machine.nix.package}/bin/nix";
      stock = "${pkgs.nix}/bin/nix";
    in
    ''
      fixed_nix = "${fixed}"
      stock_nix = "${stock}"
      assert fixed_nix != stock_nix, "the fix module did not change nix.package"

      machine.wait_for_unit("multi-user.target")
      machine.succeed("head -c 12M /dev/urandom > /tmp/blob")
      blob = machine.succeed("nix-store --add /tmp/blob").strip()
      machine.succeed(f"nix copy --to 'file:///srv/cache?compression=none' {blob}")
      machine.succeed("systemctl start cut-cache.service dead-cache.service")
      machine.wait_for_open_port(8080)
      machine.wait_for_open_port(8081)


      def fetch(nix, port, store):
          return machine.execute(
              f"{nix} copy --from http://127.0.0.1:{port} --to {store}"
              " --no-check-sigs --option download-attempts 5"
              f" --option narinfo-cache-negative-ttl 0 {blob} 2>&1"
          )


      def nar_requests(unit):
          return int(machine.succeed(
              f"journalctl -u {unit} -o cat | grep -c 'GET /nar/' || true"
          ))


      with subtest("fixed Nix finishes a download that breaks twelve times"):
          rc, out = fetch(fixed_nix, 8080, "/tmp/fixed")
          print(out)
          assert rc == 0, "fixed Nix gave up on a download that kept moving"
          machine.succeed(f"cmp /tmp/blob /tmp/fixed{blob}")
          tries = nar_requests("cut-cache")
          assert tries >= 12, f"expected at least 12 NAR requests, saw {tries}"

      with subtest("a cache that sends nothing still fails after five tries"):
          rc, out = fetch(fixed_nix, 8081, "/tmp/dead")
          print(out)
          assert rc != 0, "fixed Nix finished a download that never got data"
          tries = nar_requests("dead-cache")
          assert tries == 5, f"expected 5 NAR requests to the dead cache, saw {tries}"

      with subtest("stock Nix gives up after five tries"):
          rc, out = fetch(stock_nix, 8080, "/tmp/stock")
          print(out)
          if rc == 0:
              raise Exception(
                  "stock Nix now finishes a download whose connection breaks"
                  " more than five times; drop system/fixes/nix-download-retry"
                  " and its imports"
              )
    '';
}
