{ noctalia, libqalculate }:

# Keep the pinned upstream package and dependencies; only repair the shell's
# metadata admission and generic tray behavior (with a divider on either
# side), let notification cards be swiped away with their timer bar draining
# from the left, show Bluetooth devices by their advertised name with
# nameless ones hidden by default, treat a touch long press as a right click,
# and let battery-guard own the laptop battery warnings.
#
# Motion and scroll events reach widgets with their Wayland timestamp
# (pointer-event-time.patch), so a swipe's speed is measured on when the
# finger moved, not on when a busy shell got round to reading the event.
#
# The setup wizard saves the bundled wallpaper by its store path, which is
# gone after an update and garbage collection, so the desktop and lock screen
# lost their wallpaper and the lock screen retried the dead path on every key
# (wallpaper-store-path.patch maps it to the running build's asset).
#
# Keyboards that show a code to type (DisplayPasskey, DisplayPinCode) never
# showed it: the pairing card only appeared for requests that wait for a reply.
# Upstream fixed the passkey card (2a80c854, #4232). bluetooth-pairing.patch
# also shows the PIN while BlueZ waits for the device, closes the card when the
# device pairs or goes away, makes Cancel on a code card stop the pairing,
# re-registers the agent when bluetoothd restarts, and posts a notification
# for a request while the Bluetooth tab is not open. The panel is not opened by
# itself: any device in range could otherwise pop it up and take the keyboard.
#
# The CPU package sensor jumps 10-15 °C on every short load burst, so the bar's
# temperature flickered (47 -> 63 -> 56 -> 47 within seconds on the ZBook x2).
# cpu-temp-smoothing.patch shows an 8 s moving average of it instead.
#
# Upstream registers the Wi-Fi secret agent again whenever NM comes back
# (22ab9a4d), but takes GetSecrets from any caller and drops the request when
# the control center closes. network-secret-agent.patch only takes GetSecrets
# and CancelGetSecrets from the current NM owner, keeps the request when the
# control center closes, and posts a notification while the Network tab is not
# open.
#
# Upstream attaches to NM whenever it appears on the bus (24f85bae). A shell
# that starts while NM is down still took NM's own wpa_supplicant, which is on
# the bus but closed to the user. wpa-supplicant-access-denied.patch (ours;
# upstream has the same gap) refuses a wpa_supplicant that answers
# AccessDenied, so the NM backend waits.
#
# Touch-only machines (a tablet with its keyboard cover off) had no way to
# type the password at the lock screen. lock-osk.patch (ours) adds an
# on-screen keyboard to the lock surface itself, so it is drawn and read by
# the locked shell and nothing else sees the keys. It shows when no wired or
# built-in keyboard is attached, or on a touch, and goes away on a hardware
# key press or its hide key. It only types into the password field: no
# modifiers, clipboard, emoji, settings or launch keys, no prediction and no
# key previews above the keyboard.
#
# A click on a notification runs its "default" action, but the shell's own
# cards could not carry one: the Wi-Fi password and Bluetooth pairing cards
# said "open the control center" and a click did nothing (Plane 189).
# notification-internal-actions.patch gives those two cards a "default"
# action that opens their tab. Upstream has one slot for clicks on the
# shell's own cards (setInternalActionCallback) and the calendar took it, so
# the patch moves the slot to the application: the two cards open their tab,
# every other click goes on to the calendar as before.
#
# The shell rewrites its state-dir settings.toml when it migrates the config
# to a newer version, and an older build refuses a file from a newer one, so
# a rollback to an older generation lost the bar settings (Q110/Q112).
# settings-per-version.patch keeps one copy per config version
# (settings.v<N>.toml); a build seeds its own copy from the newest older one
# and migrates only that, so the older build still finds the file it wrote.
# `noctalia config settings-path` prints the copy in use.
#
# libqalculate 5.10 frees its random state on exit even when nothing ever
# created it, so the shell crashes every time it stops. Upstream fixed that in
# 931c2aa9 (5.11); drop the patch once nixpkgs ships libqalculate 5.11 or newer.
(noctalia.override {
  libqalculate = libqalculate.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./libqalculate-randstate.patch ];
  });
}).overrideAttrs
  (old: {
    patches = (old.patches or [ ]) ++ [
      ./media-tray.patch
      ./pointer-axis-frame.patch
      ./pointer-event-time.patch
      ./tray-trailing-separator.patch
      ./notification-swipe.patch
      ./bluetooth.patch
      ./touch-long-press.patch
      ./notification-progress-left.patch
      ./battery-notify.patch
      ./wallpaper-store-path.patch
      ./bluetooth-pairing.patch
      ./cpu-temp-smoothing.patch
      ./network-secret-agent.patch
      ./wpa-supplicant-access-denied.patch
      ./lock-osk.patch
      ./notification-internal-actions.patch
      ./settings-per-version.patch
    ];
  })
