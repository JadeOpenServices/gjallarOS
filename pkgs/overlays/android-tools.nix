final: prev:
let
  lib = prev.lib;
in
{
  # adb uses the libusb that android-tools bundles. With the patch, a phone
  # that USBGuard allows after it was plugged in reaches a running adb
  # server; see tests/nix/adb-usbguard-allow.nix.
  android-tools =
    assert lib.assertMsg (prev.android-tools.version == "35.0.2")
      "gjallarOS android-tools overlay was written for 35.0.2; check whether ${prev.android-tools.version} still bundles a libusb that ignores a device being authorized after plug-in, and update or remove pkgs/overlays/android-tools.nix.";
    prev.android-tools.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/android-tools-libusb-authorized-later.patch ];
    });
}
