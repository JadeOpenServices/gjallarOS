{ noctalia-greeter }:

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
# internal-panel-cursor.patch starts the cursor on the laptop panel (eDP,
# LVDS, DSI) while the lid is open, not on a TV the layout puts first, so
# the touchpad moves a pointer the user can see.
noctalia-greeter.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [
    ./staging-snapshot.patch
    ./menu-click.patch
    ./wlr-log-level.patch
    ./osk.patch
    ./runtime-dir.patch
    ./internal-panel-cursor.patch
  ];
})
