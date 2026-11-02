# Disks with a filesystem from another system open read-only (Plane 286,
# Q181). A writable mount is not harmless there: ntfs3 marks a volume dirty
# and rewrites what it thinks are wrong link counts during the mount, exFAT
# sets its dirty bit, and a volume the other system left half shut down
# (fast startup, hibernation) can be damaged. With these defaults plugging a
# disk in and opening it never writes to it. A writable mount stays possible
# on purpose: udisks puts its defaults first and the caller's options after
# them, so a client that asks for "rw" (the "Allow writing" button) gets it.
#
# udisks replaces a built-in "<fs>_defaults" line with the one here, so each
# line repeats the built-in options of udisks 2.11.2
# (data/builtin_mount_options.conf) and adds ro. Check
# foreign-media-readonly compares them with the source of the udisks in
# use, so an update that changes them shows up there.
{
  services.udisks2.settings."mount_options.conf".defaults = {
    vfat_defaults = "uid=$UID,gid=$GID,shortname=mixed,utf8=1,showexec,flush,ro";
    exfat_defaults = "uid=$UID,gid=$GID,iocharset=utf8,errors=remount-ro,ro";
    "ntfs:ntfs_defaults" = "uid=$UID,gid=$GID,windows_names,ro";
    "ntfs:ntfs3_defaults" = "uid=$UID,gid=$GID,ro";
    hfsplus_defaults = "uid=$UID,gid=$GID,nls=utf8,ro";
  };
}
