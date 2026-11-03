# chronyd -s with an rtcfile (NixOS enableRTCTrimming) sends READY only after
# eight RTC update interrupts. An RTC that accepts the interrupt request but
# never interrupts kept chronyd in "activating" until systemd killed it, again
# on every restart. Since Linux 7.1 rtc-cmos picks the ACPI alarm over the HPET
# RTC emulation when the FADT allows it, while the HPET keeps IRQ 8 in legacy
# mode, so QEMU q35 machines get no RTC interrupts at all. The initial trim now
# gives up after 20 s and chronyd starts as without an rtcfile. Upstream
# report: book 753.
# Test: checks chrony-rtc-no-interrupts; remove when upstream chronyd starts
# without RTC interrupts.
final: prev:

let
  lib = prev.lib;
in
{
  chrony =
    assert lib.assertMsg (prev.chrony.version == "4.8")
      "GjallarOS chrony overlay was written for 4.8; check whether ${prev.chrony.version} still waits forever for RTC interrupts with -s and an rtcfile and update or remove pkgs/overlays/chrony.nix.";
    prev.chrony.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/chrony-rtc-initial-trim-timeout.patch ];
    });
}
