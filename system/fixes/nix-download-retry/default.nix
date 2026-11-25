# Large downloads fail after five broken connections even though every
# connection moved them forward (Plane 431).
#
# Linked from: system/default.nix (the installed system) and
# system/installer-media/live.nix (the stick: nixos-install runs the nix on
# its PATH, which is config.nix.package).
# Written 2026-11-25 against nixpkgs 8ce73368, Nix 2.34.9.
# Test: tests/nix/nix-download-retry-fix.nix (flake check nix-download-retry-fix).
#
# What goes wrong
#   The v2026.10.69 stick install stopped with
#     error: unable to download '...nar.zst': Stream error in the HTTP/2
#     framing layer (16)
#   while fetching a 390 MB path from the release cache. Nix pauses a
#   transfer while the store cannot keep up with the data. GitHub's release
#   download host drops a stream that the client has not read for 70 to 130
#   seconds (curl probe, 2026-11-25: 70 s pause fine, 130 s and 310 s pause
#   "HTTP/2 stream 1 reset by server"; over HTTP/1.1 the 130 s pause ends
#   with "end of response with ... bytes missing"). On a busy disk that
#   happens again and again. Nix resumes each time with a Range request (the
#   last try started at 386 of 390 MB), but in
#   src/libstore/filetransfer.cc finish() every failed try adds one to
#   `attempt`, and after `download-attempts` (5) tries it gives up, no
#   matter how much each try fetched.
#
# What this does
#   nix-retry-counts-stalled-tries-only.patch resets the count when a try
#   wrote data, so only tries in a row without progress count. A download
#   that keeps moving finishes; a dead server still fails after five tries.
#   The NAR hash check is untouched, so a resumed download that comes out
#   wrong is still refused. Cost: Nix is built from source with the patch
#   (the release cache carries the build); every program that links libnix
#   gets a new store path.
#
# Upstream
#   Nix master at b84c8d54 (checked 2026-11-25) still counts every failed
#   try; the retry code moved into maybeRetry(), so the patch needs a rebase
#   there. NixOS/nix#13074 reported the same "retrying from offset" break in
#   2025 and was closed as a network error. The patch and the report wait in
#   the human-session queue (book 753, page 1290).
#
# Remove when
#   - the test's stock machine finishes a download whose connection breaks
#     more than five times: drop this folder and its two imports.
#   - Nix is no longer 2.34.9 (the assertion below stops the build): check
#     whether finish() still counts tries that made progress. If it does,
#     update the patch and the version; if not, drop this folder.
{
  config,
  lib,
  pkgs,
  ...
}:

let
  nix = pkgs.nix;
in
lib.mkIf config.nix.enable {
  nix.package =
    assert lib.assertMsg (nix.version == "2.34.9")
      "system/fixes/nix-download-retry was written for Nix 2.34.9; check whether ${nix.version} still counts download tries that made progress, then update or remove the fix.";
    nix.appendPatches [ ./nix-retry-counts-stalled-tries-only.patch ];
}
