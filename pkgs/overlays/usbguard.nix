# usbguard rejects USB devices whose endpoint descriptors are longer than 7 or
# 9 bytes, which the kernel and libusb accept, so a MediaTek phone in flash
# mode never reaches USB review (upstream USBGuard/usbguard#686).
final: prev:

let
  lib = prev.lib;
in
{
  usbguard =
    assert lib.assertMsg (prev.usbguard.version == "1.1.4")
      "GjallarOS usbguard overlay was written for 1.1.4; check whether ${prev.usbguard.version} still rejects long endpoint descriptors (USBGuard/usbguard#686) and update or remove pkgs/overlays/usbguard.nix.";
    prev.usbguard.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/usbguard-long-endpoint-descriptor.patch ];
    });
}
