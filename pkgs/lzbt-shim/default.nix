# lzbt from the lanzaboote flake input with the shim first stage patch
# (Plane 293 step 3). Without --shim-directory it behaves like upstream, so
# every machine uses this build and switching shim on or off stays a rebuild.
{ lzbt }:
lzbt.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [ ../patches/lanzaboote-shim-first-stage.patch ];
})
