{ noctalia, libqalculate }:

# Keep the pinned upstream package and dependencies; only repair the shell's
# metadata admission and generic tray behavior, let notification cards be
# swiped away with their timer bar draining from the left, show Bluetooth
# devices by their advertised name with nameless ones hidden by default,
# treat a touch long press as a right click, trust Bluetooth devices after
# pairing (upstream 27f2e349, drop it once the pin includes that commit), and
# let battery-guard own the laptop battery warnings.
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
      ./notification-swipe.patch
      ./bluetooth.patch
      ./touch-long-press.patch
      ./bluetooth-trust.patch
      ./notification-progress-left.patch
      ./battery-notify.patch
      ./notification-input-region.patch
    ];
  })
