{
  config,
  lib,
  pkgs,
  settings,
  ...
}:
# An extra boot entry to read the firmware flash chip when the firmware hides
# the SPI controller from Linux: flashrom's spi-intel route (linux_mtd) then
# has no /dev/mtd, and its `internal` programmer maps chipset registers
# through /dev/mem, which IO_STRICT_DEVMEM blocks (ZBook x2 G4, ODDC
# items 61 and 92). `iomem=relaxed` lifts that for this entry only.
#
# The disk opens with the recovery key or disk passphrase there:
# lanzaboote.nix leaves this UKI out of the TPM policy, so the TPM never
# hands the disk key to the weaker boot, and rebuilds stop in it. Kernel
# lockdown (job 25.5) must stay off in this entry only.
lib.mkIf (settings.firmwareFlashReadEntry or false) {
  specialisation.gjallar-flash-read.configuration = {
    system.nixos.tags = [ "firmware-flash-read" ];
    # No TPM try at all: the unseal always fails here, and with a startup
    # PIN and tries=0 systemd took that as a wrong PIN and asked again
    # forever (e2e-fw13, 2026-10-12). Filtered from the parent's options
    # because installers before 2026-10-09 also wrote tpm2-device=auto into
    # generated/hardware.nix. tpm2-measure-pcr=yes still keeps the token
    # module out, so the passphrase prompt comes first.
    boot.initrd.luks.devices = lib.mapAttrs (_: device: {
      crypttabExtraOpts = lib.mkForce (lib.remove "tpm2-device=auto" device.crypttabExtraOpts);
    }) config.boot.initrd.luks.devices;
    boot.kernelParams = [
      "iomem=relaxed"
      "gjallar.flash-read"
    ];
    environment.systemPackages = [ pkgs.flashrom ];
    environment.etc."gjallar/flash-read".text = ''
      Firmware flash read entry. Read only:
        sudo flashrom -p internal -r <file>
      The dump stays on this machine. Restart into the normal entry
      before rebuilding.
    '';
  };
}
