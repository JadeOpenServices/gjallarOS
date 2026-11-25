# systemd with tpm2-setup-lockout.patch (Plane 333).
#
# Linked from: nothing yet. Draft: only the test uses it until the way to
# ship it is decided (R55, wiki page 752).
# Written 2026-11-25 against systemd 260.5.
# Test: tests/nix/systemd-tpm2-lockout-fix.nix (flake check
# systemd-tpm2-lockout-fix).
#
# What goes wrong
#   After too many wrong PINs the security chip (TPM2) refuses PIN-protected
#   use for a while (dictionary attack lockout). systemd-tpm2-setup.service
#   anchors each NvPCR at boot with an extend that counts as such use. In
#   lockout the TPM answers TPM2_RC_LOCKOUT, tpm2-setup logs "Failed to
#   extend NvPCR index with anchor secret", exits 1 and the unit fails. The
#   next rebuild then ends with "the following units failed:
#   systemd-tpm2-setup.service" (seen on the T480s, 2026-11-23).
#
# What this does
#   tpm2-setup-lockout.patch turns TPM2_RC_LOCKOUT on the extend into
#   ENOLCK. tpm2-setup then skips the NvPCR with a notice when the extend or
#   the unseal of the stored anchor secret (already ENOLCK in stock systemd)
#   hits the lockout, exits EX_TEMPFAIL (75) and lets both tpm2-setup units
#   accept that status. The NvPCR stays unanchored and
#   refuses measurements until a later boot anchors it, exactly as after the
#   failure today; only the unit result changes. In the test VM the lockout
#   already stops the unseal; the T480s got one step further, to the extend.
#   The VM test covers the unseal route only.
#
# Upstream
#   systemd v262 dropped the anchor secret (PR #42796, commit f0f36877,
#   merged 2026-07-28): NvPCRs are now set up only in the initrd, with a
#   signed write policy, and there is no unseal or anchor extend left to
#   hit the lockout. 260.x and 261.x still have the old code. The patch and
#   a report for the stable branches wait in the human-session queue
#   (book 753).
#
# Remove when
#   - nixpkgs moves to systemd 262 or later (the assertion below stops the
#     build): drop this folder and its test, then check once on a TPM in
#     lockout that systemd-tpm2-setup.service still passes.
#   - the test's stock run stops failing in lockout (it says so): drop this
#     folder.
{ pkgs }:

let
  inherit (pkgs) lib systemd;
in
assert lib.assertMsg (systemd.version == "260.5")
  "system/fixes/systemd-tpm2-lockout was written for systemd 260.5; check whether ${systemd.version} still fails tpm2-setup when the TPM is in dictionary attack lockout, then update or remove the fix.";
systemd.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ [ ./tpm2-setup-lockout.patch ];
})
