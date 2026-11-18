# Proves that the gjallar builder's running system came from upstream NixOS
# and from its own configuration, not from the gjallarOS cache that CI fills.
# Otherwise the "second, independent build" could share its tools with CI.
#
# Runs inside the builder as root, given on stdin through the guest agent:
#   qm guest exec <vmid> --pass-stdin 1 --timeout 3000 -- /run/current-system/sw/bin/bash -s <full commit> < deployment/builder/check-store.sh
# <full commit> is the gjallarOS commit the builder image or the last
# nixos-rebuild came from.
#
# Every store path of /run/current-system either has the same NAR hash as
# the one cache.nixos.org serves, or is built again here (nix-store --check)
# and comes out bit for bit the same. Prints one line per path that fails
# and a summary; exit 0 only when none fails.
set -euo pipefail
PATH=/run/current-system/sw/bin
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
cd -- "$tmp"

rev=${1:-}
[[ $rev =~ ^[0-9a-f]{40}$ ]] || { echo "usage: bash -s <full 40-character commit>" >&2; exit 2; }
flake="github:JadeOpenServices/gjallarOS/$rev?dir=deployment/builder#nixosConfigurations.gjallaros-build.config.system.build.toplevel"

sys=$(readlink -f /run/current-system)
want=$(nix eval --raw "$flake.outPath")
[ "$want" = "$sys" ] || { echo "the running system is $sys, commit $rev gives $want" >&2; exit 2; }
drvs=$(nix-store -qR "$(nix eval --raw "$flake.drvPath")" | grep '\.drv$')

same=0 rebuilt=0 bad=0
for p in $(nix-store -qR "$sys"); do
  base=${p#/nix/store/}
  hash=${base%%-*}
  local_nar=$(nix-store -q --hash "$p")
  # 404: not on cache.nixos.org. Anything else (network) stops the check.
  code=$(curl -sS -o narinfo -w '%{http_code}' --max-filesize 65536 "https://cache.nixos.org/$hash.narinfo")
  if [ "$code" = 200 ]; then
    if [ "NarHash: $local_nar" = "$(grep -m1 '^NarHash: ' narinfo)" ]; then
      same=$((same + 1))
    else
      bad=$((bad + 1)); echo "DIFFERS from cache.nixos.org: $p"
    fi
    continue
  fi
  [ "$code" = 404 ] || { echo "cache.nixos.org answered $code for $p" >&2; exit 2; }
  drv=""
  for c in $(printf '%s\n' "$drvs" | grep -F -- "-${base#*-}.drv"); do
    nix-store -q --outputs "$c" | grep -qxF "$p" && { drv=$c; break; }
  done
  if [ -z "$drv" ]; then
    bad=$((bad + 1)); echo "NO DERIVATION in commit $rev: $p"
  elif nix-store --realise --check "$drv" >/dev/null 2>check.err; then
    rebuilt=$((rebuilt + 1))
  else
    bad=$((bad + 1)); echo "REBUILD DIFFERS: $p"; tail -n 3 check.err
  fi
done
echo "same_as_cache.nixos.org=$same rebuilt_identical=$rebuilt failed=$bad"
[ "$bad" = 0 ]
