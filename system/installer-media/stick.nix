{
  config,
  lib,
  pkgs,
  ...
}:
# The installer stick (Plane 344/350). The same image for everyone: built
# from the source without generated/ or user.config.json, US keyboard until
# the stick menu asks (CP-D). For now tty1 is the live login, like the
# recovery ISO.
let
  media = config.system.build.gjallarMedia;
  isoName = "gjallar-stick-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}.iso";

  # No gjallar.iso=: stage 1 takes the volume labelled GJALLAR_STICK, and
  # gjallar.verity= decides whether its store may run.
  uki = media.mkUki {
    name = "gjallar-stick.efi";
    prettyName = "gjallarOS installer";
    cmdline = "";
  };
in
{
  imports = [ ./live.nix ];

  # targetdisk.bootMediaLabel knows this label; the installer never offers
  # the stick as a target.
  isoImage.volumeID = "GJALLAR_STICK";
  image.baseName = lib.mkForce (lib.removeSuffix ".iso" isoName);

  networking.hostName = "gjallar-stick";

  system.build.gjallarStickIso = pkgs.callPackage ../../pkgs/gjallar-stick-iso {
    inherit uki isoName;
    inherit (media) store storeVerity;
    inherit (config.isoImage) volumeID;
    inherit (pkgs.stdenv.hostPlatform) efiArch;
  };
}
