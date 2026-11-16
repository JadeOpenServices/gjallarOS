# The release package (CP-H): the makers get the key, version and floor,
# RELEASE and SHA256SUMS have the form the makers and the stick read, and
# a README with another fingerprint or maker hash, or a key the makers
# would not take, fails the build. Keys and the image are SYNTHETIC test
# fixtures made here; no release key is used.
{ pkgs }:
let
  inherit (pkgs) lib runCommand openssl;
  mkRelease = pkgs.callPackage ../../pkgs/gjallar-release { };

  key =
    name: script:
    runCommand "release-test-key-${name}" { nativeBuildInputs = [ openssl ]; } ''
      mkdir $out
      ${script}
    '';
  rsaKey =
    bits: exp:
    key "rsa${toString bits}-e${toString exp}" ''
      openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:${toString bits} \
        -pkeyopt rsa_keygen_pubexp:${toString exp} -out k.pem 2>/dev/null
      openssl pkey -in k.pem -pubout -out $out/release.pub
    '';
  good = rsaKey 3072 65537;
  # A second key: the same derivation would give the same key.
  other = key "other" ''
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out k.pem 2>/dev/null
    openssl pkey -in k.pem -pubout -out $out/release.pub
  '';

  stickIso = runCommand "release-test-iso" { } ''
    mkdir -p $out/iso
    printf 'SYNTHETIC installer image\n' >$out/iso/gjallar-stick-test.iso
  '';
  gjlogs = pkgs.callPackage ../../pkgs/gjallar-gjlogs-image { };

  args = {
    version = "v2026.11.3";
    floor = "v2026.11.1";
    date = "2026-11-16";
    keyFile = "${good}/release.pub";
    makeStickSh = ../../scripts/installation/make-stick.sh;
    makeStickPs1 = ../../scripts/installation/make-stick.ps1;
    readme = null;
    inherit stickIso gjlogs;
  };
  base = mkRelease args;

  readme =
    name: body: filter:
    runCommand "README-${name}.md" { } ''
      {
        echo '# SYNTHETIC README'
        ${body}
      } | ${filter} >$out
    '';
  block = makers: ''
    echo '```text'
    echo 'Release key fingerprint (SHA-256):'
    cat ${makers}/readme-fingerprint
    echo '```'
    echo
    echo '| Maker | SHA-256 of the current release |'
    echo '| --- | --- |'
    cat ${makers}/readme-makers
  '';
  goodReadme = readme "good" (block base.makers) "cat";
  withReadme = r: mkRelease (args // { readme = r; });
  withKey = k: mkRelease (args // { keyFile = k; });

  fails =
    name: drv: message:
    let
      failed = pkgs.testers.testBuildFailure drv;
    in
    runCommand "release-test-${name}" { } ''
      grep -qF -- ${lib.escapeShellArg message} ${failed}/testBuildFailure.log || {
        echo "${name}: want '${message}' in:" >&2
        cat ${failed}/testBuildFailure.log >&2
        exit 1
      }
      touch $out
    '';
  evalFails = name: x: lib.assertMsg (!(builtins.tryEval (builtins.seq x.drvPath true)).success) name;

  checks = [
    # A README showing another key, or lacking a maker row.
    (fails "other-key"
      (withReadme (readme "other" (block (withKey "${other}/release.pub").makers) "cat")).readmeCheck
      "shows another release key fingerprint"
    )
    (fails "no-ps1-row"
      (withReadme (readme "no-ps1" (block base.makers) "sed '/make-stick.ps1/d'")).readmeCheck
      "lacks this maker row"
    )
    (fails "stale-row"
      (withReadme (
        readme "stale" (block base.makers)
          "sed 's/| [0-9a-f]\\{64\\} |$/| ${lib.fixedWidthString 64 "0" ""} |/'"
      )).readmeCheck
      "lacks this maker row"
    )
    (fails "two-blocks"
      (withReadme (readme "two" (block base.makers + block base.makers) "cat")).readmeCheck
      "needs exactly one"
    )
    # Keys the makers and stickrelease would not take.
    (fails "short-key" (withKey "${rsaKey 2048 65537}/release.pub").makers "want at least 3072")
    (fails "exponent-3" (withKey "${rsaKey 3072 3}/release.pub").makers "exponent is not 65537")
    (fails "private-key"
      (withKey "${key "private" "openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out $out/release.pub 2>/dev/null"}/release.pub")
      .makers
      "holds a private key"
    )
    (fails "rewrapped"
      (withKey "${key "rewrapped" ''
        { head -n 1 ${good}/release.pub; sed '1d;$d' ${good}/release.pub | tr -d '\n' | fold -w 76; echo
          tail -n 1 ${good}/release.pub; } >$out/release.pub
      ''}/release.pub").makers
      "not in the form openssl writes"
    )
    (fails "ec-key"
      (withKey "${key "ec" ''
        openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-384 -out k.pem 2>/dev/null
        openssl pkey -in k.pem -pubout -out $out/release.pub
      ''}/release.pub").makers
      "not an RSA key"
    )
  ];
in
assert evalFails "floor newer than the release"
  (mkRelease (args // { floor = "v2026.11.4"; })).makers;
assert evalFails "bad version" (mkRelease (args // { version = "v2026.13.1"; })).makers;
assert evalFails "bad date" (mkRelease (args // { date = "20261116"; })).makers;
let
  r = (withReadme goodReadme).release;
in
runCommand "release-test" { nativeBuildInputs = [ openssl ]; } ''
  : ${lib.concatStringsSep " " checks}
  cd ${r}
  ls | sort >$TMPDIR/files
  printf '%s\n' RELEASE SHA256SUMS gjallarOS-installer-v2026.11.3.iso gjlogs.img make-stick.ps1 make-stick.sh |
    cmp - $TMPDIR/files
  [ "$(cat RELEASE)" = "$(printf 'version=v2026.11.3\ndate=2026-11-16\nfloor=v2026.11.1')" ]
  sha256sum -c --strict --quiet SHA256SUMS
  [ "$(cut -d ' ' -f 3 SHA256SUMS | tr '\n' ' ')" = "gjallarOS-installer-v2026.11.3.iso gjlogs.img RELEASE make-stick.sh make-stick.ps1 " ]
  grep -qvE '^[0-9a-f]{64}  [A-Za-z0-9._-]+$' SHA256SUMS && exit 1

  # The sh maker carries the key, version and floor.
  awk "/^    local release_key='-----BEGIN PUBLIC KEY-----\$/ { sub(/^    local release_key='/, \"\"); f = 1 }
    f { print } f && /^-----END PUBLIC KEY-----'\$/ { exit }" make-stick.sh | sed '$s/.$//' |
    cmp - ${good}/release.pub
  grep -qx "    local maker_release='v2026.11.3' maker_floor='v2026.11.1'" make-stick.sh

  # The ps1 maker carries the modulus and exponent of the same key.
  modulus=$(openssl rsa -pubin -in ${good}/release.pub -noout -modulus)
  want=$(printf '%s' "''${modulus#Modulus=}" | basenc --base16 -d | base64 -w 0)
  grep -qxF "    \$keyModulus = '$want'" make-stick.ps1
  grep -qxF "    \$keyExponent = 'AQAB'" make-stick.ps1
  grep -qxF "    \$makerRelease = 'v2026.11.3'" make-stick.ps1
  grep -qxF "    \$makerFloor = 'v2026.11.1'" make-stick.ps1

  # The README fingerprint is the SHA-256 of the DER key, as the sh maker
  # prints it.
  fp=$(openssl pkey -pubin -in ${good}/release.pub -outform DER | sha256sum | cut -d ' ' -f 1)
  [ "$(tr -d ' \n' <${base.makers}/readme-fingerprint)" = "''${fp^^}" ]
  [ "$(wc -l <${base.makers}/readme-fingerprint)" = 2 ]
  touch $out
''
