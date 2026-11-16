# shellcheck shell=bash
# Builds the oldest release tag that has no builder statement yet and pushes
# the statement (SHA256SUMS + SHA256SUMS.sshsig) to the statements
# repository. Runs as root from gjallar-builder-statement.service: only root
# reads the signing key and the push token. Everything that touches the
# tagged code (listing tags, evaluating and building the flake) runs as the
# unprivileged build user and hands back plain text on stdout.
#
# Environment (set by the unit): SOURCE_URL, STATEMENTS_URL, FIRST_TAG,
# BUILD_USER, KEY_DIR.

umask 077

namespace=gjallaros-release
identity=gjallar-builder
key=$KEY_DIR/statement-key
token_file=$KEY_DIR/statements-token
failed_dir=$KEY_DIR/failed
bare=$KEY_DIR/statements.git
max_tries=3

tag_re='^v[0-9]{4}\.(0[1-9]|1[0-2])\.[0-9]{1,6}$'
names=(gjallarOS-installer-@TAG@.iso gjlogs.img RELEASE make-stick.sh make-stick.ps1)

tag=
# Counts a failed try for the tag being built; after max_tries the tag waits
# until someone removes its file in $failed_dir.
note_failure() {
  local n=0
  [ -f "$failed_dir/$tag" ] && n=$(head -c 16 -- "$failed_dir/$tag")
  [[ $n =~ ^[0-9]+$ ]] || n=0
  mkdir -p -- "$failed_dir"
  printf '%s\n' "$((n + 1))" >"$failed_dir/$tag.tmp"
  mv -f -- "$failed_dir/$tag.tmp" "$failed_dir/$tag"
}

fail() {
  echo "gjallar-builder: $*" >&2
  if [ -n "$tag" ]; then note_failure; fi
  exit 1
}

# A root-owned regular file, not a link, at most $2 bytes.
read_capped() {
  local f=$1 cap=$2
  [ -f "$f" ] && [ ! -L "$f" ] || fail "$f is not a regular file"
  [ "$(stat -c %s -- "$f")" -le "$cap" ] || fail "$f is larger than $cap bytes"
  cat -- "$f"
}

as_builder() {
  setpriv --reuid="$BUILD_USER" --regid="$BUILD_USER" --clear-groups \
    --no-new-privs --reset-env -- \
    env PATH="$PATH" HOME="/var/lib/$BUILD_USER" XDG_CACHE_HOME="/var/lib/$BUILD_USER/cache" \
    "$@"
}

[ -f "$key" ] && [ ! -L "$key" ] || fail "no statement key yet (gjallar-builder-statement-key.service makes it)"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

# Release tags of the source repository, peeled to their commits. The build
# user fetches them; root only parses the text it prints.
as_builder git ls-remote --tags "$SOURCE_URL" 'refs/tags/v*' |
  head -c 1048576 >"$work/tags.raw"
declare -A rev=()
while IFS=$'\t' read -r h ref; do
  [[ $h =~ ^[0-9a-f]{40}$ ]] || continue
  t=${ref#refs/tags/}
  peeled=0
  if [[ $t == *'^{}' ]]; then
    t=${t%'^{}'}
    peeled=1
  fi
  [[ $t =~ $tag_re ]] || continue
  # An annotated tag lists its tag object and then its commit (^{}).
  if [ "$peeled" = 1 ] || [ -z "${rev[$t]:-}" ]; then
    rev[$t]=$h
  fi
done <"$work/tags.raw"

# Statements already pushed. The repository is public; fetching needs no
# token. Nothing is checked out: the tree is only read and written through
# git objects, so no file or link in that repository lands on this disk.
[ -d "$bare" ] || git init -q --bare "$bare"
git --git-dir="$bare" fetch -q --depth 1 --no-tags "$STATEMENTS_URL" "+refs/heads/main:refs/remotes/origin/main"
base=$(git --git-dir="$bare" rev-parse --verify -q refs/remotes/origin/main) || fail "the statements repository has no main branch"

# Oldest first, from FIRST_TAG on: older tags have no gjallar-release output.
next=
while read -r t; do
  [ -n "$t" ] || continue
  [ "$(printf '%s\n%s\n' "$FIRST_TAG" "$t" | sort -V | head -n 1)" = "$FIRST_TAG" ] || continue
  git --git-dir="$bare" cat-file -e "$base:$t/$identity/SHA256SUMS" 2>/dev/null && continue
  if [ -f "$failed_dir/$t" ]; then
    n=$(head -c 16 -- "$failed_dir/$t")
    [[ $n =~ ^[0-9]+$ ]] || n=$max_tries
    [ "$n" -lt "$max_tries" ] || continue
  fi
  next=$t
  break
done < <(printf '%s\n' "${!rev[@]}" | sort -V)
[ -n "$next" ] || {
  echo "gjallar-builder: no tag waits for a statement"
  exit 0
}
tag=$next
commit=${rev[$tag]}
# The new files go to $tag/$identity/. Anything else already at those paths
# (a file or link) would make the commit fail after a long build.
for p in "$tag" "$tag/$identity"; do
  typ=$(git --git-dir="$bare" cat-file -t "$base:$p" 2>/dev/null) || continue
  [ "$typ" = tree ] || fail "$p in the statements repository is a $typ, not a folder"
done
echo "gjallar-builder: building $tag ($commit)"

# The same flake output CI builds, pinned to the commit the tag named when
# it was listed. A moved tag makes the fetch fail instead of building
# something else.
if ! as_builder nix build --no-link --print-out-paths \
  "git+$SOURCE_URL?ref=refs/tags/$tag&rev=$commit&submodules=1#gjallar-release" \
  >"$work/out" 2>"$work/build.log"; then
  tail -n 40 "$work/build.log" >&2
  fail "the build of $tag failed"
fi
out=$(head -c 4096 "$work/out")
[[ $out =~ ^/nix/store/[0-9a-z]{32}-[^/[:space:]]+$ ]] || fail "the build printed no single store path"
[ -d "$out" ] && [ ! -L "$out" ] || fail "$out is not a directory"

read_capped "$out/SHA256SUMS" 65536 >"$work/SHA256SUMS"
read_capped "$out/RELEASE" 4096 >"$work/RELEASE"
grep -qx "version=$tag" "$work/RELEASE" || fail "RELEASE of the build does not say version=$tag"

# Exactly the five files CI publishes, one line each, in this order.
want=()
for n in "${names[@]}"; do want+=("${n/@TAG@/$tag}"); done
mapfile -t lines <"$work/SHA256SUMS"
[ "${#lines[@]}" = "${#want[@]}" ] || fail "SHA256SUMS has ${#lines[@]} lines, want ${#want[@]}"
for i in "${!want[@]}"; do
  [[ ${lines[$i]} =~ ^[0-9a-f]{64}\ \ (.+)$ ]] && [ "${BASH_REMATCH[1]}" = "${want[$i]}" ] ||
    fail "SHA256SUMS line $((i + 1)) is not the hash of ${want[$i]}"
done

ssh-keygen -q -Y sign -f "$key" -n "$namespace" <"$work/SHA256SUMS" >"$work/SHA256SUMS.sshsig"
printf '%s namespaces="%s" %s\n' "$identity" "$namespace" "$(cut -d' ' -f1,2 "$key.pub")" >"$work/allowed_signers"
ssh-keygen -Y verify -f "$work/allowed_signers" -I "$identity" -n "$namespace" \
  -s "$work/SHA256SUMS.sshsig" <"$work/SHA256SUMS" >/dev/null ||
  fail "the new signature does not verify"

# One commit on top of the fetched main with the two new files.
export GIT_DIR=$bare GIT_INDEX_FILE=$work/index
export GIT_AUTHOR_NAME=$identity GIT_AUTHOR_EMAIL=$identity@invalid
export GIT_COMMITTER_NAME=$identity GIT_COMMITTER_EMAIL=$identity@invalid
# A fixed date: the commit tells nothing about the builder's clock or zone.
export GIT_AUTHOR_DATE='1970-01-01T00:00:00+0000' GIT_COMMITTER_DATE='1970-01-01T00:00:00+0000'
git read-tree "$base"
sums_blob=$(git hash-object -w -- "$work/SHA256SUMS")
sig_blob=$(git hash-object -w -- "$work/SHA256SUMS.sshsig")
git update-index --add --cacheinfo "100644,$sums_blob,$tag/$identity/SHA256SUMS"
git update-index --add --cacheinfo "100644,$sig_blob,$tag/$identity/SHA256SUMS.sshsig"
tree=$(git write-tree)
new=$(git commit-tree "$tree" -p "$base" -m "$tag: $identity statement")

# The token goes to git through its environment, never on a command line.
tok=$(read_capped "$token_file" 1024 | tr -d '\n')
[[ $tok =~ ^github_pat_[A-Za-z0-9_]+$ ]] || fail "$token_file does not hold a fine-grained token"
auth=$(printf 'x-access-token:%s' "$tok" | base64 -w0)
unset tok
GIT_CONFIG_COUNT=2 \
  GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0='' \
  GIT_CONFIG_KEY_1=http.https://github.com/.extraHeader GIT_CONFIG_VALUE_1="Authorization: Basic $auth" \
  git push -q "$STATEMENTS_URL" "$new:refs/heads/main" ||
  fail "pushing the statement for $tag failed; the next run tries again"
rm -f -- "$failed_dir/$tag"
echo "gjallar-builder: pushed the statement for $tag"
