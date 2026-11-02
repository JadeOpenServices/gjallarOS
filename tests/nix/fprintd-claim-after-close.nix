# fprintd patched by pkgs/overlays/fprintd.nix next to the stock one. A
# client claims the libfprint virtual reader and leaves without Release, as
# pam_fprintd does after a match; the reader then takes 3 s to close, as the
# fw13 Goodix reader takes a moment. A second client claims as soon as the
# first is gone, as the next sudo does. The patched fprintd answers that
# Claim once the close is done; the stock one refuses it with AlreadyInUse.
# When the stock one answers it too, the test says to drop the patch.
{ pkgs, stockFprintd }:
let
  socketPath = "/var/lib/fprint/virtual.sock";
  client = pkgs.writers.writePython3 "fprintd-claim-after-close" {
    libraries = [ pkgs.python3Packages.pygobject3 ];
    flakeIgnore = [ "E501" ];
  } (builtins.readFile ./fprintd-claim-after-close.py);
  run = "GI_TYPELIB_PATH=${pkgs.glib.out}/lib/girepository-1.0 ${client} ${socketPath}";

  node = fprintd: {
    services.fprintd = {
      enable = true;
      package = fprintd;
    };
    systemd.services.fprintd.environment = {
      FP_VIRTUAL_DEVICE = socketPath;
      G_MESSAGES_DEBUG = "all";
    };
    # Test only: the client runs as root outside any session, which the
    # fprintd policy refuses. GjallarOS itself grants no such rule.
    security.polkit.enable = true;
    security.polkit.extraConfig = ''
      polkit.addRule(function (action, subject) {
        if (action.id.indexOf("net.reactivated.fprint.device.") == 0 &&
            subject.user == "root") {
          return polkit.Result.YES;
        }
      });
    '';
  };
in
pkgs.testers.runNixOSTest {
  name = "fprintd-claim-after-close";

  nodes.patched = node pkgs.fprintd;
  nodes.stock = node stockFprintd;

  testScript = ''
    start_all()
    for m in [patched, stock]:
        m.wait_for_unit("multi-user.target")

    out = patched.succeed("${run}").strip()
    print("patched: " + out)
    patched.succeed("journalctl -u fprintd -b | grep -q 'waits for the device to close'")
    assert out.startswith("claimed "), "patched fprintd refused the Claim during close: " + out
    assert float(out.split()[1]) >= 1.0, "Claim was answered before the slow close finished: " + out

    out = stock.succeed("${run}").strip()
    print("stock: " + out)
    assert out == "refused net.reactivated.Fprint.Error.AlreadyInUse", (
        "stock fprintd now answers a Claim made while the device closes (" + out + "); "
        "drop pkgs/overlays/fprintd.nix and pkgs/patches/fprintd-claim-waits-for-close.patch"
    )
  '';
}
