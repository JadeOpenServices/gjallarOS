# pkgs/overlays/plymouth.nix next to a stock plymouth. Both show a small
# script theme that paints the screen red and turns it green in its quit
# function. The patch runs the quit function when the splash goes idle, which
# quit --retain-splash (greetd, unl0kr) and deactivate share; the test reads
# the frame after deactivate, while plymouthd still holds the screen. The
# patched one shows green; the stock one never runs the quit function and
# stays red. When the stock one shows green too, the test says to drop the
# patch.
{ pkgs, stockPlymouth }:
let
  # The script plugin paints nothing, not even the background, while a theme
  # has no sprite, so the theme puts a small one in the corner, away from the
  # pixel the test reads.
  theme = pkgs.runCommand "plymouth-theme-retaintest" { nativeBuildInputs = [ pkgs.imagemagick ]; } ''
    dir=$out/share/plymouth/themes/retaintest
    mkdir -p $dir
    magick -size 8x8 xc:white $dir/dot.png
    cat > $dir/retaintest.script <<'EOF'
    dot = Sprite(Image("dot.png"));
    Window.SetBackgroundTopColor(1, 0, 0);
    Window.SetBackgroundBottomColor(1, 0, 0);

    fun quit_callback () {
      Window.SetBackgroundTopColor(0, 1, 0);
      Window.SetBackgroundBottomColor(0, 1, 0);
    }

    Plymouth.SetQuitFunction(quit_callback);
    EOF
    cat > $dir/retaintest.plymouth <<EOF
    [Plymouth Theme]
    Name=retaintest
    ModuleName=script

    [script]
    ImageDir=$dir
    ScriptFile=$dir/retaintest.script
    EOF
  '';

  # The NixOS module, so plymouthd finds its themes and plugins where it does on
  # a real boot.
  node = plymouth: {
    boot.plymouth = {
      enable = true;
      package = plymouth;
      themePackages = [ theme ];
      theme = "retaintest";
    };
    # Test VMs boot with console=ttyS0. With a serial console in /proc/consoles
    # plymouthd only drives text terminals and never opens the DRM card, so the
    # screen keeps showing the text console.
    boot.kernelParams = [ "plymouth.ignore-serial-consoles" ];
  };
in
pkgs.testers.runNixOSTest {
  name = "boot-splash-retain";

  nodes.patched = node pkgs.plymouth;
  nodes.stock = node stockPlymouth;

  testScript = ''
    import subprocess

    both = [patched, stock]


    def colour(m, name):
        m.screenshot(name)
        out = subprocess.run(
            ["${pkgs.imagemagick}/bin/magick", f"{m.out_dir}/{name}.png",
             "-gravity", "center", "-crop", "1x1+0+0", "-format", "%[fx:int(255*r)],%[fx:int(255*g)],%[fx:int(255*b)]", "info:"],
            check=True, capture_output=True, text=True,
        ).stdout
        return tuple(int(v) for v in out.split(","))


    def shows(m, name, want):
        got = colour(m, name)
        m.log(f"{name}: {got}")
        return got == want


    start_all()
    for m in both:
        m.wait_for_unit("multi-user.target")
        # As a unit, the way boot starts it: plymouthd writes to /dev/tty, and
        # the tty of the test shell is the driver's console.
        m.succeed("systemd-run --unit=plymouthd-test plymouthd --mode=boot --no-daemon")
        m.wait_until_succeeds("plymouth --ping", timeout=30)
        m.succeed("timeout 30 plymouth show-splash")

    with subtest("both show the theme's red frame"):
        for m in both:
            retry(lambda last: shows(m, f"{m.name}-shown", (255, 0, 0)), timeout_seconds=60)

    # Deactivate goes idle the same way quit does, but plymouthd stays up and
    # its frame stays on screen. Once plymouthd exits, the kernel puts the
    # text console back here, which a real boot covers with the greeter.
    for m in both:
        m.succeed("plymouth deactivate")

    with subtest("patched plymouth leaves the frame of the quit function"):
        assert shows(patched, "patched-quit", (0, 255, 0)), "patched plymouth did not run the quit function on quit --retain-splash"

    with subtest("stock plymouth still skips the quit function"):
        assert shows(stock, "stock-quit", (255, 0, 0)), "stock plymouth now runs the quit function on quit --retain-splash; drop pkgs/overlays/plymouth.nix"

    with subtest("quit --retain-splash still ends plymouthd"):
        for m in both:
            m.succeed("plymouth quit --retain-splash --wait")
            m.fail("plymouth --ping")
  '';
}
