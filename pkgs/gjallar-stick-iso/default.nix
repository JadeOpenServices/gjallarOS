{
  lib,
  runCommand,
  xorriso,
  mtools,
  libfaketime,
  dosfstools,
  util-linux,
  # From system.build.gjallarMedia (system/installer-media/base.nix).
  uki,
  store,
  storeVerity,
  volumeID,
  isoName,
  efiArch,
  # pkgs/gjallar-shim, x86_64 only; null boots the UKI directly.
  shim ? null,
  # Release assets stay below 2 GiB, and a 2 GB stick keeps room for GJLOGS.
  maxSize ? 1900000000,
}:
# The installer stick image. Its own xorriso step instead of iso-image.nix:
# - MBR only, no GPT. The makers add the GJLOGS partition in MBR slot 3
#   (design 344/350, L76); a GPT would hide it from Windows and Linux.
# - Partition 2 is the EFI system partition and El Torito's EFI image at
#   once. UEFI only: no BIOS boot code, no GRUB.
# - The UKI carries the store's dm-verity root hash on its command line, so
#   stage 1 checks every block the store hands out.
# - With shim (design 344/350 CP-C, Q1): BOOTX64.EFI is Debian's signed
#   shim and the UKI sits next to it as grubx64.efi, the name shim loads.
#   Under Secure Boot with only the Microsoft keys shim cannot verify the
#   UKI and opens MokManager (mmx64.efi), where the user enrolls this
#   UKI's hash. The installer removes that hash again after the install.
#   No fbx64.efi: fallback would write a boot entry for the stick into
#   the machine's NVRAM.
# Fixed dates, FAT serial and file order keep the build reproducible, same
# as upstream's efiImg and make-iso9660-image.sh.
let
  bootFile = "BOOT${lib.toUpper efiArch}.EFI";
  placeBoot =
    if shim == null then
      ''
        cp ${uki} esp/EFI/BOOT/${bootFile}
      ''
    else
      assert efiArch == "x64";
      ''
        cp ${shim}/share/shim/shimx64.efi esp/EFI/BOOT/${bootFile}
        cp ${shim}/share/shim/mmx64.efi esp/EFI/BOOT/mmx64.efi
        cp ${uki} esp/EFI/BOOT/grubx64.efi
      '';
in
runCommand isoName
  {
    nativeBuildInputs = [
      xorriso
      mtools
      libfaketime
      dosfstools
      util-linux
    ];
    strictDeps = true;
  }
  ''
    mkdir -p esp/EFI/BOOT
    ${placeBoot}
    # Fallback (fb*.efi) writes NVRAM boot entries; the stick must not.
    if find esp -iname 'fb*.efi' | grep -q .; then
      echo "fallback loader on the stick ESP" >&2
      exit 1
    fi
    ls -l esp/EFI/BOOT
    find esp -exec touch --date=2000-01-01 {} +

    # Files rounded up to whole MiB, plus 10% for FAT, in whole MiB.
    mib=$((1024 * 1024))
    used=$(( $(du -s --block-size=1M --apparent-size esp | tr -cd '[:digit:]') * mib ))
    size=$(( (used * 110 / 100 / mib + 1) * mib ))
    truncate --size=$size efi.img
    mkfs.vfat --invariant -i 12345678 -n EFIBOOT efi.img
    (
      cd esp
      for d in $(find EFI -type d | sort); do
        faketime "2000-01-01 00:00:00" mmd -i ../efi.img "::/$d"
      done
      for f in $(find EFI -type f | sort); do
        mcopy -pm -i ../efi.img "$f" "::/$f"
      done
    )
    fsck.vfat -vn efi.img

    mkdir -p $out/iso
    iso=$out/iso/${isoName}
    xorriso -volume_date all_file_dates "=$SOURCE_DATE_EPOCH" \
      -as mkisofs \
      -iso-level 3 \
      -volid ${volumeID} \
      -full-iso9660-filenames -joliet -r \
      -graft-points \
      -partition_offset 16 \
      --mbr-force-bootable \
      -append_partition 2 0xef efi.img \
      -e --interval:appended_partition_2:all:: \
      -no-emul-boot \
      -o "$iso" \
      /nix-store.squashfs=${store} \
      /nix-store.squashfs.verity=${storeVerity}/nix-store.squashfs.verity

    # No GPT anywhere: slot 3 has to stay free for GJLOGS.
    sfdisk -l "$iso"
    [ "$(blkid -p -o value -s PTTYPE "$iso")" = dos ] || { echo "partition table is not MBR" >&2; exit 1; }
    if dd if="$iso" bs=512 skip=1 count=1 status=none | head -c 8 | grep -aqF "EFI PART"; then
      echo "image carries a GPT header" >&2
      exit 1
    fi

    size=$(stat -c %s "$iso")
    echo "image size: $size bytes (cap ${toString maxSize})"
    [ "$size" -le ${toString maxSize} ] || { echo "image is $size bytes, over the ${toString maxSize} cap" >&2; exit 1; }
  ''
