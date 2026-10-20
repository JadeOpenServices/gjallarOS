{ noctalia, libqalculate }:

# Keep the pinned upstream package and dependencies; only repair the shell's
# metadata admission and generic tray behavior (with a divider on either
# side), let notification cards be swiped away with their timer bar draining
# from the left, show Bluetooth devices by their advertised name with
# nameless ones hidden by default, treat a touch long press as a right click,
# trust Bluetooth devices after pairing (upstream 27f2e349, drop it once the
# pin includes that commit), and let battery-guard own the laptop battery
# warnings.
#
# A card shown while the shell is still starting finished its slide-in within one
# stalled frame and kept the input region of its start position, so clicks went
# through to the window below. Upstream fixed that in f5e744f0; drop
# notification-input-region.patch once the pin includes it.
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
# Upstream fixed the passkey card in 2a80c854 (#4232). bluetooth-pairing.patch
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
# Wi-Fi password prompts died after the first rebuild switch: NM restarts and
# forgets its agents, and the shell registered only once, so NM answered "No
# agents were available". network-secret-agent.patch backports upstream
# 22ab9a4d (#4402, in 5.2.1) to register again whenever NM comes back, only
# takes GetSecrets and CancelGetSecrets from the current NM owner, keeps the
# request when the control center closes, and posts a notification while the
# Network tab is not open. Drop it once the pin is 5.2.1 or newer, except for
# the sender check and the notification.
#
# The shell picked its Wi-Fi backend once at startup. When NM was not on the bus
# yet (or restarting), it fell back to wpa_supplicant, which the user may not
# talk to, and never looked at NM again, so the Wi-Fi list stayed empty ("no
# networks in range") until the shell restarted. networkmanager-follow-bus.patch
# backports upstream 24f85bae to attach to NM whenever it appears on the bus and
# detach when it leaves. Drop it once the pin includes that commit.
# That alone does not cover a shell that starts while NM is down: NM's own
# wpa_supplicant is on the bus, closed to the user, and the fallback took it
# anyway. wpa-supplicant-access-denied.patch (ours; upstream has the same gap)
# refuses a wpa_supplicant that answers AccessDenied, so the NM backend waits.
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
      ./bluetooth-trust.patch
      ./notification-progress-left.patch
      ./battery-notify.patch
      ./notification-input-region.patch
      ./wallpaper-store-path.patch
      ./bluetooth-pairing.patch
      ./cpu-temp-smoothing.patch
      ./network-secret-agent.patch
      ./networkmanager-follow-bus.patch
      ./wpa-supplicant-access-denied.patch
    ];
  })
