{ runCommand, dosfstools }:

# The GJLOGS partition the USB stick makers add after the installer image
# (Plane 350, design 4.2). A 32 MiB FAT16 file system with a fixed volume
# id, so every build has the same bytes and the release signs one hash.
# The installer finds it by label and saves its log there. The makers
# write it as it is; they never format anything on the stick.
runCommand "gjallar-gjlogs-image" { nativeBuildInputs = [ dosfstools ]; } ''
  mkdir -p $out
  mkfs.fat -C -F 16 -n GJLOGS -i 474a4c47 --invariant $out/gjlogs.img 32768
  size=$(stat -c %s $out/gjlogs.img)
  [ "$size" = 33554432 ] || { echo "gjlogs.img is $size bytes, want 33554432" >&2; exit 1; }
''
