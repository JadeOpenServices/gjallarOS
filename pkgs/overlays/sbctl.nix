# sbctl makes its certificates without a Subject Key Identifier. Go adds one
# only to CA certificates, and RFC 5280 4.2.1.2 says other certificates
# SHOULD carry one too. mokutil --import looks the gjallarOS key up in the
# kernel keyring by it and prints "Failed to get Subject Key ID" in the
# middle of the install when it is missing. The patch sets the identifier
# the way Go does for CAs (SHA-1 of the subjectPublicKey bits) for file, TPM
# and YubiKey keys. Upstream queue: wiki book 753.
# Test: TestFileKeySubjectKeyID from the patch runs in postCheck; remove
# when sbctl sets SubjectKeyId itself.
final: prev:

let
  lib = prev.lib;
in
{
  sbctl =
    assert lib.assertMsg (prev.sbctl.version == "0.18")
      "gjallarOS sbctl overlay was written for 0.18; check whether ${prev.sbctl.version} sets a Subject Key Identifier on the certificates it makes and update or remove pkgs/overlays/sbctl.nix.";
    prev.sbctl.overrideAttrs (old: {
      patches = (old.patches or [ ]) ++ [ ../patches/sbctl-subject-key-id.patch ];
      # The stock check phase tests only the root package.
      postCheck = (old.postCheck or "") + ''
        go test ./backend -run '^TestFileKeySubjectKeyID$' -v
      '';
    });
}
