{
  lib,
  runCommand,
  openssl,
}:

# The files of one gjallarOS release (CP-H, Plane 344/350): the installer
# image, GJLOGS, both USB stick makers with the release key filled in,
# RELEASE and SHA256SUMS. CI signs SHA256SUMS apart from this build; no
# private key ever comes near it.
#
# makers: the two makers and the README lines they need. It does not build
#   the image, so the README can be updated before a release build.
# readmeCheck: README.md shows this key's fingerprint and these makers'
#   SHA-256 (Q229/Q236); a release with a stale README does not build.
# release: the folder CI uploads as the release assets.
{
  # vYYYY.MM.N: the tag, system/release.
  version,
  # The oldest release still safe to write, deployment/release-floor.
  floor,
  # YYYY-MM-DD (UTC) of the commit, the same on every builder.
  date,
  # The release public key, PEM (deployment/release-key.pem).
  keyFile,
  makeStickSh,
  makeStickPs1,
  readme,
  stickIso,
  gjlogs,
}:

let
  # The makers' ver_key: vYYYY.MM.N with N up to 6 digits.
  verKey =
    what: name:
    let
      m = builtins.match "v([0-9]{4})\\.(0[1-9]|1[0-2])\\.([0-9]{1,6})" name;
    in
    if m == null then
      throw "gjallar-release: ${what} '${name}' is not a release name like v2026.10.1"
    else
      lib.concatStrings [
        (builtins.elemAt m 0)
        (builtins.elemAt m 1)
        (lib.fixedWidthString 6 "0" (builtins.elemAt m 2))
      ];
in
assert lib.assertMsg (
  !(verKey "version" version < verKey "floor" floor)
) "gjallar-release: the floor ${floor} is newer than the release ${version}";
assert lib.assertMsg (
  builtins.match "[0-9]{4}-[0-9]{2}-[0-9]{2}" date != null
) "gjallar-release: date '${date}' is not YYYY-MM-DD";
let
  makers = runCommand "gjallar-release-makers-${version}" { nativeBuildInputs = [ openssl ]; } ''
    fail() { echo "gjallar-release: $*" >&2; exit 1; }

    # Only a public key goes into the makers: one PEM block, nothing else
    # in the file, written the way openssl writes it, so the base64 body
    # the sh maker decodes is exactly the DER key the fingerprint names.
    cp ${keyFile} key.pem
    ! grep -q PRIVATE key.pem || fail "the release key file holds a private key"
    [ "$(grep -c -- '^-----' key.pem)" = 2 ] &&
      [ "$(head -n 1 key.pem)" = "-----BEGIN PUBLIC KEY-----" ] &&
      [ "$(tail -n 1 key.pem)" = "-----END PUBLIC KEY-----" ] ||
      fail "the release key file is not one PEM public key"
    openssl pkey -pubin -in key.pem -out canon.pem || fail "openssl cannot read the release key"
    cmp -s key.pem canon.pem || fail "the release key file is not in the form openssl writes"
    # Like stickrelease.minKeyBits and the makers: RSA, 3072 bits or more,
    # exponent 65537 (make-stick.ps1 carries it as AQAB).
    openssl rsa -pubin -in key.pem -noout -text >key.txt || fail "the release key is not an RSA key"
    bits=$(sed -n 's/^Public-Key: (\([0-9]*\) bit)$/\1/p' key.txt)
    [ -n "$bits" ] && [ "$bits" -ge 3072 ] || fail "the release key has ''${bits:-an unknown number of} bits, want at least 3072"
    grep -qx 'Exponent: 65537 (0x10001)' key.txt || fail "the release key exponent is not 65537"
    modulus=$(openssl rsa -pubin -in key.pem -noout -modulus)
    modulus=''${modulus#Modulus=}
    [ "''${#modulus}" = $((bits / 4)) ] && [[ $modulus =~ ^[0-9A-F]+$ ]] ||
      fail "the release key modulus has an unexpected form"

    body=$(sed '1d;$d' key.pem)
    modulus64=$(printf '%s' "$modulus" | basenc --base16 -d | base64 -w 0)

    mkdir -p $out
    for f in sh ps1; do
      case $f in
      sh) src=${makeStickSh} ;;
      ps1) src=${makeStickPs1} ;;
      esac
      for p in KEY KEY_MODULUS KEY_EXPONENT VERSION FLOOR; do
        n=$( (grep -o "@GJALLAR_RELEASE_''${p}@" $src || true) | wc -l)
        case $f/$p in
        sh/KEY_MODULUS | sh/KEY_EXPONENT | ps1/KEY) [ "$n" = 0 ] ;;
        *) [ "$n" = 1 ] ;;
        esac || fail "make-stick.$f has @GJALLAR_RELEASE_''${p}@ $n times"
      done
    done
    substitute ${makeStickSh} $out/make-stick.sh \
      --replace-fail "@GJALLAR_RELEASE_KEY@" "$body" \
      --replace-fail "@GJALLAR_RELEASE_VERSION@" ${version} \
      --replace-fail "@GJALLAR_RELEASE_FLOOR@" ${floor}
    substitute ${makeStickPs1} $out/make-stick.ps1 \
      --replace-fail "@GJALLAR_RELEASE_KEY_MODULUS@" "$modulus64" \
      --replace-fail "@GJALLAR_RELEASE_KEY_EXPONENT@" AQAB \
      --replace-fail "@GJALLAR_RELEASE_VERSION@" ${version} \
      --replace-fail "@GJALLAR_RELEASE_FLOOR@" ${floor}
    ! grep -q '@GJALLAR_' $out/make-stick.sh $out/make-stick.ps1 || fail "a maker still holds a placeholder"
    chmod 0444 $out/make-stick.sh $out/make-stick.ps1

    # The lines README.md must carry: the fingerprint block as the makers
    # print it (SHA-256 of the DER key, two lines of eight groups), then
    # the maker table rows.
    fp=$(openssl pkey -pubin -in key.pem -outform DER | sha256sum)
    fp=''${fp%% *}
    fp=''${fp^^}
    [ "$(printf '%s' "$body" | tr -d '\n' | base64 -d | sha256sum | cut -d ' ' -f 1)" = "''${fp,,}" ] ||
      fail "the key body and the DER key differ"
    for i in 0 32; do
      line=" "
      for ((j = i; j < i + 32; j += 4)); do line+=" ''${fp:j:4}"; done
      printf '%s\n' "$line"
    done >$out/readme-fingerprint
    for f in make-stick.sh make-stick.ps1; do
      sum=$(sha256sum <$out/$f)
      printf '| `%s` | %s |\n' $f "''${sum%% *}"
    done >$out/readme-makers
  '';

  readmeCheck = runCommand "gjallar-release-readme-${version}" { } ''
    fail() { echo "gjallar-release: README.md $*" >&2; exit 1; }
    awk '$0 == "Release key fingerprint (SHA-256):" { n++; getline; print; getline; print }
      END { exit (n != 1) }' ${readme} >fingerprint ||
      fail "needs exactly one 'Release key fingerprint (SHA-256):' block"
    cmp -s fingerprint ${makers}/readme-fingerprint ||
      fail "shows another release key fingerprint; put in these two lines:$(printf '\n'; cat ${makers}/readme-fingerprint)"
    while IFS= read -r line; do
      [ "$(grep -cxF -- "$line" ${readme})" = 1 ] ||
        fail "lacks this maker row (once, as it is): $line"
    done <${makers}/readme-makers
    touch $out
  '';

  release = runCommand "gjallar-release-${version}" { } ''
    fail() { echo "gjallar-release: $*" >&2; exit 1; }
    : ${readmeCheck}
    isos=(${stickIso}/iso/*.iso)
    [ "''${#isos[@]}" = 1 ] && [ -f "''${isos[0]}" ] || fail "want exactly one image in ${stickIso}/iso"
    # The makers write at most this many bytes (iso_max/$isoMax).
    [ "$(stat -L -c %s "''${isos[0]}")" -le 2000000000 ] || fail "the image is over 2000000000 bytes"
    [ "$(stat -L -c %s ${gjlogs}/gjlogs.img)" = 33554432 ] || fail "gjlogs.img is not 33554432 bytes"

    mkdir -p $out
    cd $out
    ln -s "''${isos[0]}" gjallarOS-installer-${version}.iso
    ln -s ${gjlogs}/gjlogs.img gjlogs.img
    ln -s ${makers}/make-stick.sh make-stick.sh
    ln -s ${makers}/make-stick.ps1 make-stick.ps1
    # Exactly the three lines the makers and the stick read.
    printf 'version=%s\ndate=%s\nfloor=%s\n' ${version} ${date} ${floor} >RELEASE
    sha256sum -- gjallarOS-installer-${version}.iso gjlogs.img RELEASE make-stick.sh make-stick.ps1 >SHA256SUMS
  '';
in
{
  inherit makers readmeCheck release;
}
