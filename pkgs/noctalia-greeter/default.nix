{ noctalia-greeter, systemdLibs }:

# Keep the pinned upstream package; the root apply-appearance helper reads a
# root-owned snapshot of the staging folder so staged symlinks cannot leak
# files into the greeter state. menu-click.patch keeps a click on a control
# alive when the press moves focus and the menus get rebuilt.
# wlr-log-level.patch stops wlroots debug lines (one per frame with a
# software cursor) from flooding the journal.
# osk.patch lets the compositor start an on-screen keyboard
# ($NOCTALIA_GREETER_OSK, wvkbd) for touch-only machines. Layer-shell and
# virtual-keyboard are offered to that one client only.
# runtime-dir.patch keeps the session's own /run/user dir instead of a fixed
# /tmp path that any local user could create first and so block the login
# screen.
# internal-panel-first.patch puts the laptop panel (eDP, LVDS, DSI) first
# while the lid is open: it sits at (0,0) with the greeter and the cursor,
# and the other screens go to its right. Before, a TV sorting first by name
# took both, so the touchpad moved a pointer the user could not see.
# osk-auto-show.patch starts that keyboard shown when no built-in or wired
# keyboard is attached, and shows it again when the last one is unplugged
# (udev ID_INPUT_KEYBOARD, not virtual, not Bluetooth). It needs libudev.
# display-ready.patch creates $NOCTALIA_GREETER_DISPLAY_READY once the
# compositor has its first frame on a screen, so the boot splash can let go.
# The upstream polkit action for the apply-appearance helper is dropped:
# it names the same helper as org.gjallaros.greeter-sync (wayland.nix) but
# lets inactive and remote sessions ask too, and pkexec picked it over ours.
# rm without -f, so a renamed upstream file stops the build instead of
# leaving two actions again.
noctalia-greeter.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [
    ./staging-snapshot.patch
    ./menu-click.patch
    ./wlr-log-level.patch
    ./osk.patch
    ./runtime-dir.patch
    ./internal-panel-first.patch
    ./osk-auto-show.patch
    ./display-ready.patch
  ];
  buildInputs = (old.buildInputs or [ ]) ++ [ systemdLibs ];
  postInstall = (old.postInstall or "") + ''
    rm "$out/share/polkit-1/actions/org.noctalia.greeter.apply-appearance.policy"
  '';
})
