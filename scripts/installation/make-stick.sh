#!/usr/bin/env bash
# gjallarOS USB stick maker for Linux.
#
#   curl -fsSL https://github.com/JadeOpenServices/gjallarOS/releases/latest/download/make-stick.sh | bash
#   bash make-stick.sh [--release vYYYY.MM.N]
#
# Downloads the installer stick image, checks its signature against the key
# below, writes it to a USB stick the user picks, reads it back and adds a
# small GJLOGS partition for install logs. Only the write runs as root: the
# root part is the string in root_code, passed to bash as an argument, and
# the image reaches it on stdin. Nothing is run from a file, and root opens
# no path the user can change (loopholes L70, L71, L72, L76).
#
# Everything sits in one function that the last line calls, so a cut-off
# download runs nothing.

gjallar_make_stick() {
    set -euo pipefail
    export LC_ALL=C
    umask 077

    local releases='https://github.com/JadeOpenServices/gjallarOS/releases'
    # RSA public key for release signatures (RSA-PSS, SHA-256, salt 32).
    # Its fingerprint (SHA-256 of the DER key) is in the README, apart from
    # the release (Plane 350, Q5); the maker shows it before it downloads.
    local release_key='-----BEGIN PUBLIC KEY-----
@GJALLAR_RELEASE_KEY@
-----END PUBLIC KEY-----'
    # The release this copy came from and the oldest release still safe
    # then. Like the newest release this computer used, they are a floor
    # the server cannot push the maker under (rollback guard, Q228/Q231).
    local maker_release='@GJALLAR_RELEASE_VERSION@' maker_floor='@GJALLAR_RELEASE_FLOOR@'
    local img_size=33554432 iso_max=2000000000

    local log_file="" work="" tty_fd=""

    log() {
        [[ -n $log_file ]] || return 0
        printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >>"$log_file" 2>/dev/null || true
    }
    say() { printf '%s\n' "$@"; }
    fail() {
        printf 'ERROR: %s\n' "$1" >&2
        log "ERROR: $1"
        log "GJA-3098 maker.run.failed"
        exit 1
    }
    ask() {
        printf '%s' "$1" >/dev/tty
        REPLY=""
        IFS= read -r -u "$tty_fd" REPLY || REPLY=""
    }
    finish() {
        local rc=$?
        trap - EXIT INT TERM
        if [[ -n $work && -d $work ]]; then
            rm -rf -- "$work"
        fi
        log "GJA-3099 maker.run.finished code=$rc"
        exit "$rc"
    }
    usage() {
        say "Usage: make-stick.sh [--release vYYYY.MM.N]" \
            "" \
            "Writes the gjallarOS installer to a USB stick. Without --release it" \
            "uses the newest stable release."
    }

    [[ -n ${HOME:-} && $HOME == /* ]] || {
        printf 'ERROR: HOME is not set.\n' >&2
        exit 1
    }
    local state="${XDG_STATE_HOME:-$HOME/.local/state}"
    [[ $state == /* ]] || state="$HOME/.local/state"
    if mkdir -p -- "$state/gjallaros" 2>/dev/null; then
        log_file="$state/gjallaros/make-stick.log"
        printf '\nmake-stick %s UTC\n' "$(date -u '+%Y-%m-%d %H:%M:%S')" >>"$log_file" 2>/dev/null || log_file=""
    fi
    trap finish EXIT
    trap 'exit 130' INT TERM
    log "GJA-3001 maker.run.started os=linux"

    local release=""
    while (($#)); do
        case $1 in
        --release)
            (($# >= 2)) || {
                usage >&2
                exit 2
            }
            release=$2
            shift 2
            ;;
        --release=*)
            release=${1#*=}
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage >&2
            exit 2
            ;;
        esac
    done
    if [[ -n $release && ! $release =~ ^v[0-9]{4}\.[0-9]{2}\.[0-9]+$ ]]; then
        printf 'ERROR: A release name looks like v2026.10.1.\n' >&2
        exit 2
    fi

    # A release build puts the key body in; an unbuilt copy still holds
    # the placeholder, which is not base64.
    local key_body=${release_key#-----BEGIN PUBLIC KEY-----}
    key_body=${key_body%-----END PUBLIC KEY-----}
    key_body=${key_body//$'\n'/}
    # Release names the guard compares: vYYYY.MM.N with N up to 6 digits.
    ver_key() { # name -> sortable key, or fail
        [[ $1 =~ ^v([0-9]{4})\.(0[1-9]|1[0-2])\.([0-9]{1,6})$ ]] || return 1
        printf '%s%s%06d' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "$((10#${BASH_REMATCH[3]}))"
    }
    ver_lt() { [[ $(ver_key "$1") < $(ver_key "$2") ]]; }
    if [[ -z $key_body || $key_body == *[!A-Za-z0-9+/=]* ]] ||
        ! ver_key "$maker_release" >/dev/null || ! ver_key "$maker_floor" >/dev/null; then
        fail "This copy of the maker is not from a release. Download make-stick.sh from a gjallarOS release."
    fi
    local t missing=()
    for t in curl openssl sha256sum lsblk flock dd od stat readlink mktemp date head mv; do
        command -v "$t" >/dev/null 2>&1 || missing+=("$t")
    done
    ((${#missing[@]} == 0)) || fail "These programs are missing: ${missing[*]}. Install them and run the maker again."
    exec {tty_fd}</dev/tty || fail "The maker needs a terminal to ask which stick to write."

    say "gjallarOS USB stick maker" "" \
        "This downloads the gjallarOS installer, checks its signature and writes" \
        "it to a USB stick. Everything on that stick is erased." ""

    # Q229/Q236: the user compares the key fingerprint with the README.
    # This catches an old copy or one built with another key. It cannot
    # catch a changed script: that can print the right fingerprint and
    # skip every check after it. Only the download-first way in the
    # README (compare the script's SHA-256 before running it) covers that.
    local key_fp i fp_lines=("" "")
    key_fp=$(printf '%s' "$key_body" | openssl base64 -d -A | sha256sum) ||
        fail "The maker could not compute its release key fingerprint."
    key_fp=${key_fp%% *}
    key_fp=${key_fp^^}
    [[ $key_fp =~ ^[0-9A-F]{64}$ ]] || fail "The maker could not compute its release key fingerprint."
    for ((i = 0; i < 64; i += 4)); do
        fp_lines[i / 32]+=" ${key_fp:i:4}"
    done
    say "Release key fingerprint (SHA-256):" " ${fp_lines[0]}" " ${fp_lines[1]}" "" \
        "Compare it with \"Release key fingerprint\" in the gjallarOS README."
    ask "Type yes if it matches: "
    if [[ $REPLY != [yY][eE][sS] ]]; then
        log "GJA-3003 maker.key.declined"
        fail "The fingerprint was not confirmed; nothing was downloaded or written. Get the maker again through the gjallarOS README."
    fi
    log "GJA-3002 maker.key.confirmed"
    say ""
    if [[ -z $release ]]; then
        ask "Press Enter to use the newest stable release, or type A for advanced options: "
        if [[ $REPLY == [aA] ]]; then
            while :; do
                ask "Release name, for example v2026.10.1 (Enter for stable): "
                [[ -z $REPLY || $REPLY =~ ^v[0-9]{4}\.[0-9]{2}\.[0-9]+$ ]] && break
                say "A release name looks like v2026.10.1."
            done
            release=$REPLY
        fi
    fi
    local base="$releases/latest/download"
    [[ -z $release ]] || base="$releases/download/$release"

    local cache="${XDG_CACHE_HOME:-$HOME/.cache}"
    [[ $cache == /* ]] || cache="$HOME/.cache"
    mkdir -p -- "$cache" || fail "Cannot create $cache for the download."
    work=$(mktemp -d -- "$cache/gjallar-make-stick.XXXXXXXX") || fail "Cannot create a download folder in $cache."

    fetch() { # url file max-bytes progress
        local try
        for try in 1 2 3; do
            if curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fL -C - \
                --max-filesize "$3" "$4" -o "$work/$2" -- "$1"; then
                return 0
            fi
            ((try == 3)) || sleep 3
        done
        fail "Download failed three times: $1"
    }
    bad_download() {
        rm -rf -- "$work"
        log "GJA-3012 maker.signature.bad"
        fail "$1 is not the file we published. Nothing was written. Please report this line with the release name."
    }

    say "Downloading the release checksums..."
    fetch "$base/SHA256SUMS" SHA256SUMS 65536 -sS
    fetch "$base/SHA256SUMS.sig" SHA256SUMS.sig 4096 -sS
    printf '%s\n' "$release_key" >"$work/release.pem"
    openssl dgst -sha256 -sigopt rsa_padding_mode:pss -sigopt rsa_pss_saltlen:32 \
        -verify "$work/release.pem" -signature "$work/SHA256SUMS.sig" "$work/SHA256SUMS" \
        >/dev/null 2>&1 || bad_download SHA256SUMS

    local line iso_name="" iso_sha="" img_sha="" iso_tag="" rel_sha=""
    while IFS= read -r line; do
        [[ $line =~ ^([0-9a-f]{64})\ \ ([A-Za-z0-9._-]+)$ ]] || bad_download SHA256SUMS
        case ${BASH_REMATCH[2]} in
        gjlogs.img)
            [[ -z $img_sha ]] || bad_download SHA256SUMS
            img_sha=${BASH_REMATCH[1]}
            ;;
        RELEASE)
            [[ -z $rel_sha ]] || bad_download SHA256SUMS
            rel_sha=${BASH_REMATCH[1]}
            ;;
        make-stick.sh | make-stick.ps1) ;;
        *)
            local sum=${BASH_REMATCH[1]} name=${BASH_REMATCH[2]}
            [[ -z $iso_name && $name =~ ^gjallarOS-installer-(v[0-9]{4}\.[0-9]{2}\.[0-9]+)\.iso$ ]] ||
                bad_download SHA256SUMS
            iso_name=$name iso_sha=$sum iso_tag=${BASH_REMATCH[1]}
            ;;
        esac
    done <"$work/SHA256SUMS"
    [[ -n $iso_name && -n $img_sha && -n $rel_sha ]] || bad_download SHA256SUMS
    [[ -z $release || $iso_tag == "$release" ]] || bad_download SHA256SUMS

    # RELEASE, signed through SHA256SUMS: exactly
    #   version=vYYYY.MM.N  date=YYYY-MM-DD (UTC)  floor=vYYYY.MM.N
    # floor is the oldest release that is still safe to write.
    fetch "$base/RELEASE" RELEASE 512 -sS
    local sum
    sum=$(sha256sum -- "$work/RELEASE")
    [[ ${sum%% *} == "$rel_sha" ]] || bad_download RELEASE
    local -a rel=()
    mapfile -t -n 4 rel <"$work/RELEASE"
    ((${#rel[@]} == 3)) &&
        [[ ${rel[0]} == "version=$iso_tag" && ${rel[1]} =~ ^date=([0-9]{4}-[0-9]{2}-[0-9]{2})$ ]] ||
        bad_download RELEASE
    local rel_date=${BASH_REMATCH[1]} rel_floor=${rel[2]#floor=} rel_day
    [[ ${rel[2]} == "floor=$rel_floor" ]] && ver_key "$iso_tag" >/dev/null &&
        ver_key "$rel_floor" >/dev/null && ! ver_lt "$iso_tag" "$rel_floor" ||
        bad_download RELEASE
    rel_day=$(date -u -d "$rel_date" +%s 2>/dev/null) || bad_download RELEASE

    # The newest release and floor this computer used. Only the user
    # reads and writes this file; root never opens it.
    local seen_file="$state/gjallaros/make-stick.seen" seen_rel=$maker_release seen_floor=$maker_floor
    local -a seen=()
    if [[ -f $seen_file ]] && mapfile -t -n 3 seen < <(head -c 256 -- "$seen_file") &&
        ((${#seen[@]} == 2)) && ver_key "${seen[0]#version=}" >/dev/null &&
        ver_key "${seen[1]#floor=}" >/dev/null &&
        [[ ${seen[0]} == version=* && ${seen[1]} == floor=* ]]; then
        ver_lt "${seen[0]#version=}" "$seen_rel" || seen_rel=${seen[0]#version=}
        ver_lt "${seen[1]#floor=}" "$seen_floor" || seen_floor=${seen[1]#floor=}
    elif [[ -e $seen_file ]]; then
        say "Ignoring $seen_file: it is not in the expected format."
    fi
    ver_lt "$rel_floor" "$seen_floor" || seen_floor=$rel_floor
    if ver_lt "$iso_tag" "$seen_floor"; then
        rm -rf -- "$work"
        log "GJA-3013 maker.release.refused reason=floor"
        fail "gjallarOS $iso_tag is older than $seen_floor, the oldest release that is still safe. Use a newer release. Nothing was written."
    fi
    if ver_lt "$iso_tag" "$seen_rel"; then
        if [[ -z $release ]]; then
            rm -rf -- "$work"
            log "GJA-3013 maker.release.refused reason=rollback"
            fail "The server offered gjallarOS $iso_tag as the newest release, but this computer already used $seen_rel. An old release offered as new can be an attack. Nothing was written."
        fi
        log "GJA-3014 maker.release.older reason=seen"
        say "Note: $iso_tag is older than $seen_rel, which this computer used before."
    fi
    if (($(date -u +%s) - rel_day > 90 * 86400)); then
        say "" "gjallarOS $iso_tag is from $rel_date, more than 90 days ago. A newer" \
            "release may exist: compare with the newest version in the README at" \
            "${releases%/releases}"
        ask "Type OLD to use it anyway, or press Enter to stop: "
        if [[ $REPLY != OLD ]]; then
            say "Nothing was written."
            exit 130
        fi
        log "GJA-3014 maker.release.older reason=date"
    fi
    say "Downloading gjallarOS installer $iso_tag..."
    fetch "$base/$iso_name" installer.iso "$iso_max" -#
    fetch "$base/gjlogs.img" gjlogs.img "$img_size" -sS
    log "GJA-3010 maker.download.finished"

    local iso_size
    iso_size=$(stat -c %s -- "$work/installer.iso")
    [[ $(stat -c %s -- "$work/gjlogs.img") == "$img_size" ]] || bad_download gjlogs.img
    ((iso_size > 0 && iso_size % 512 == 0)) || bad_download "$iso_name"
    sum=$(sha256sum -- "$work/installer.iso")
    [[ ${sum%% *} == "$iso_sha" ]] || bad_download "$iso_name"
    sum=$(sha256sum -- "$work/gjlogs.img")
    [[ ${sum%% *} == "$img_sha" ]] || bad_download gjlogs.img
    # Only a release that downloaded whole counts as used.
    ver_lt "$iso_tag" "$seen_rel" || seen_rel=$iso_tag
    local seen_tmp
    if ! seen_tmp=$(mktemp -- "$seen_file.XXXXXXXX" 2>/dev/null) ||
        ! { printf 'version=%s\nfloor=%s\n' "$seen_rel" "$seen_floor" >"$seen_tmp" &&
            mv -f -- "$seen_tmp" "$seen_file"; }; then
        [[ -z $seen_tmp ]] || rm -f -- "$seen_tmp"
        say "Note: could not save $seen_file. The check against older releases cannot remember $iso_tag."
    fi
    log "GJA-3011 maker.signature.verified"
    say "Signature and checksums are correct."

    # USB sticks: whole USB disks with a /dev/disk/by-id/usb-* name.
    local -a d_name d_id d_size d_serial d_label d_ext
    field() { # line key
        if [[ $1 =~ (^|\ )$2=\"([^\"]*)\" ]]; then
            printf '%s' "${BASH_REMATCH[2]}"
        fi
    }
    clean() { # text from the stick's own descriptors, made safe to show
        local s=${1//\\x20/ }
        s=${s//[^A-Za-z0-9 ._()+-]/}
        printf '%s' "${s:0:40}"
    }
    find_sticks() {
        d_name=() d_id=() d_size=() d_serial=() d_label=() d_ext=()
        small=0
        local l name tran type size serial model vendor link id rm ext
        while IFS= read -r l; do
            name=$(field "$l" NAME) tran=$(field "$l" TRAN) type=$(field "$l" TYPE)
            size=$(field "$l" SIZE) serial=$(field "$l" SERIAL)
            model=$(field "$l" MODEL) vendor=$(field "$l" VENDOR)
            rm=$(field "$l" RM)
            [[ $tran == usb && $type == disk && $name =~ ^sd[a-z]{1,3}$ && $size =~ ^[1-9][0-9]{0,15}$ ]] || continue
            if ((size < iso_size)); then
                small=$((small + 1))
                continue
            fi
            id=""
            for link in /dev/disk/by-id/usb-*; do
                [[ -L $link && $link != *-part[0-9]* ]] || continue
                [[ $(readlink -f -- "$link") == "/dev/$name" ]] || continue
                id=${link##*/}
                break
            done
            [[ $id =~ ^usb-[A-Za-z0-9#+.:=@_-]{1,200}$ ]] || continue
            d_name+=("$name") d_id+=("$id") d_size+=("$size")
            d_serial+=("$serial")
            d_label+=("$(clean "$vendor $model")")
            # USB hard disks and SSDs in an enclosure: not marked removable
            # or bigger than sticks usually are. ROTA is no help here: many
            # USB bridges skip the page that reports it, so sticks show 1.
            ext=""
            [[ $rm == 1 ]] || ext+=", not marked removable"
            ((size <= ext_size)) || ext+=", bigger than $(gb "$ext_size")"
            d_ext+=("${ext#, }")
        done < <(lsblk -dn -b -P -o NAME,TRAN,TYPE,SIZE,SERIAL,MODEL,VENDOR,RM)
    }
    # One line per partition: name, size, file system, label.
    show_parts() { # name
        local l pname ptype psize fs label shown=0
        while IFS= read -r l; do
            pname=$(field "$l" NAME) ptype=$(field "$l" TYPE) psize=$(field "$l" SIZE)
            fs=$(clean "$(field "$l" FSTYPE)") label=$(clean "$(field "$l" LABEL)")
            [[ $ptype == part && $pname =~ ^$1[0-9]{1,3}$ && $psize =~ ^[0-9]{1,16}$ ]] || continue
            printf '       %-8s %10s  %-8s %s\n' "$pname" "$(gb "$psize")" "${fs:-unknown}" \
                "${label:+label \"$label\"}"
            shown=$((shown + 1))
        done < <(lsblk -n -b -P -o NAME,TYPE,SIZE,FSTYPE,LABEL -- "/dev/$1" 2>/dev/null)
        ((shown > 0)) || say "       no partitions"
    }
    gb() {
        if (($1 < 1000000000)); then
            printf '%d MB' $(($1 / 1000000))
        else
            printf '%d.%d GB' $(($1 / 1000000000)) $(($1 / 100000000 % 10))
        fi
    }

    local small=0 n i ext_size=130000000000
    while :; do
        find_sticks
        n=${#d_name[@]}
        ((n > 0)) && break
        if ((small > 0)); then
            say "" "The USB sticks plugged in are too small. The installer needs $(gb "$iso_size")."
        else
            say "" "No USB stick found."
        fi
        ask "Plug in a stick and press Enter to look again, or type Q to stop: "
        [[ $REPLY != [qQ] ]] || exit 130
    done
    say "" "USB sticks:"
    for ((i = 0; i < n; i++)); do
        printf '  %d  %-40s %10s   serial %s\n' $((i + 1)) "${d_label[i]:-USB stick}" \
            "$(gb "${d_size[i]}")" "$(clean "${d_serial[i]}")"
        [[ -z ${d_ext[i]} ]] || say "       ! Looks like an external drive: ${d_ext[i]}"
        show_parts "${d_name[i]}"
    done
    local pick=""
    while :; do
        ask "Type the number of the stick to erase and write (Enter to stop): "
        [[ -n $REPLY ]] || exit 130
        if [[ $REPLY =~ ^[1-9][0-9]?$ ]] && ((REPLY <= n)); then
            pick=$((REPLY - 1))
            break
        fi
        say "Type one of the numbers 1 to $n."
    done
    log "GJA-3020 maker.disk.chosen disks=$n"

    local dev="/dev/${d_name[pick]}"
    say "" "You picked ${d_label[pick]:-USB stick}, $(gb "${d_size[pick]}")." \
        "The maker will erase everything on it, write gjallarOS installer $iso_tag" \
        "and add a small GJLOGS partition where the installer can save its log." \
        "The write needs admin rights."
    ask "Type ERASE to go on, or press Enter to stop: "
    [[ $REPLY == ERASE ]] || {
        say "Nothing was written."
        exit 130
    }
    if [[ -n ${d_ext[pick]} ]]; then
        say "" "This looks like an external hard disk or SSD, not a USB stick" \
            "(${d_ext[pick]}). Backups and other files on it are erased too."
        ask "Type EXTERNAL to erase it anyway, or press Enter to stop: "
        [[ $REPLY == EXTERNAL ]] || {
            say "Nothing was written."
            exit 130
        }
        log "GJA-3023 maker.disk.external"
    fi

    # Unmount what the desktop mounted, as the user. Root refuses the stick
    # if anything is still mounted, held or used as swap.
    local part mnt
    while read -r part mnt; do
        [[ -n $mnt && $part =~ ^/dev/sd[a-z]{1,3}[0-9]*$ ]] || continue
        command -v udisksctl >/dev/null 2>&1 &&
            udisksctl unmount --no-user-interaction -b "$part" >/dev/null 2>&1 || true
    done < <(lsblk -nrpo NAME,MOUNTPOINT -- "$dev" 2>/dev/null)

    local -a elev=()
    if command -v sudo >/dev/null 2>&1 && sudo -v; then
        elev=(sudo --)
    elif command -v run0 >/dev/null 2>&1; then
        elev=(run0 --)
    else
        log "GJA-3021 maker.elevation.declined"
        fail "No admin rights for the write (sudo or run0). Nothing was written."
    fi

    # Root part. Arguments only, no file: by-id name, serial (hex), size,
    # ISO size, ISO hash, image hash. stdin: ISO bytes, then the image.
    # Exit codes: 10 bad arguments, 11 not the picked stick, 12 in use,
    # 13 too small, 20 stream longer than signed, 21 read-back mismatch.
    # This string must not contain a single quote.
    # shellcheck disable=SC2016
    local root_code='
set -euo pipefail
export PATH=/run/current-system/sw/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C
umask 077
echo started
(( $# == 6 )) || exit 10
id=$1 serial=$2 size=$3 iso_size=$4 iso_sha=$5 img_sha=$6
[[ $id =~ ^usb-[A-Za-z0-9#+.:=@_-]{1,200}$ && $serial =~ ^([0-9a-f]{2}){0,128}$ &&
   $size =~ ^[1-9][0-9]{0,15}$ && $iso_size =~ ^[1-9][0-9]{0,11}$ &&
   $iso_sha =~ ^[0-9a-f]{64}$ && $img_sha =~ ^[0-9a-f]{64}$ ]] || exit 10
(( iso_size % 512 == 0 )) || exit 10
img_size=33554432 mib=1048576

dev=$(readlink -f -- "/dev/disk/by-id/$id") || exit 11
[[ $dev =~ ^/dev/(sd[a-z]{1,3})$ && -b $dev && ! -L $dev ]] || exit 11
k=${BASH_REMATCH[1]}
exec {lk}<"$dev" || exit 11
# udev holds a shared lock while it probes a disk that was just written;
# wait for it, as the systemd block device locking rules ask.
flock -w 15 "$lk" || exit 12
fd=/proc/self/fd/$lk
[[ -b $fd && $(stat -Lc %t:%T -- "$fd") == $(stat -Lc %t:%T -- "$dev") ]] || exit 11
info=$(lsblk -dn -b -P -o TRAN,TYPE,SIZE,SERIAL -- "$dev") || exit 11
val() { [[ $info =~ (^|\ )$1=\"([^\"]*)\" ]] && printf "%s" "${BASH_REMATCH[2]}"; }
[[ $(val TRAN) == usb && $(val TYPE) == disk && $(val SIZE) == "$size" ]] || exit 11
[[ $(printf "%s" "$(val SERIAL)" | od -An -v -tx1 | tr -d " \n") == "$serial" ]] || exit 11
[[ $(blockdev --getsize64 "$fd") == "$size" ]] || exit 11

names=() majmin=()
for s in "/sys/block/$k" "/sys/block/$k/$k"*; do
  [[ -r $s/dev ]] || continue
  names+=("/dev/${s##*/}")
  read -r mm <"$s/dev"
  majmin+=("$mm")
  if [[ -n $(ls -A -- "$s/holders" 2>/dev/null) ]]; then exit 12; fi
done
for m in "${majmin[@]}"; do
  if awk -v m="$m" "\$3 == m { f = 1 } END { exit !f }" /proc/self/mountinfo; then exit 12; fi
done
for d in "${names[@]}"; do
  if awk -v d="$d" "\$1 == d { f = 1 } END { exit !f }" /proc/self/mounts /proc/swaps; then exit 12; fi
done
if [[ -n $(lsblk -nro MOUNTPOINT -- "$dev" | tr -d "\n") ]]; then exit 12; fi

(( size >= iso_size )) || exit 13
off=$(( (iso_size + mib - 1) / mib * mib ))
logs=too-small
(( size < off + img_size )) || logs=1

written=0
wipe() { dd if=/dev/zero of="$fd" bs=1M count=1 conv=notrunc,fsync status=none || true; }
trap "rc=\$?; if (( rc != 0 && written )); then wipe; fi; exit \$rc" EXIT
hash_at() { # offset bytes
  local h
  h=$(dd if="$fd" bs=4M iflag=fullblock,skip_bytes,count_bytes skip="$1" count="$2" status=none | sha256sum) || return 1
  printf "%s" "${h%% *}"
}

written=1
dd of="$fd" bs=4M iflag=fullblock,count_bytes count="$iso_size" conv=notrunc,fsync status=progress
if [[ $logs == 1 ]]; then
  dd of="$fd" bs=4M iflag=fullblock,count_bytes count="$img_size" oflag=seek_bytes seek="$off" conv=notrunc,fsync status=none
else
  dd of=/dev/null bs=4M iflag=fullblock,count_bytes count="$img_size" status=none
fi
[[ $(dd bs=1 count=1 status=none | wc -c) == 0 ]] || exit 20
blockdev --flushbufs "$fd"
echo written
[[ $(hash_at 0 "$iso_size") == "$iso_sha" ]] || exit 21
if [[ $logs == 1 ]]; then
  [[ $(hash_at "$off" "$img_size") == "$img_sha" ]] || exit 21
fi
echo verified

if [[ $logs == 1 ]]; then
  b=()
  while read -r -a row; do b+=("${row[@]}"); done < <(dd if="$fd" bs=512 count=2 status=none | od -An -v -tx1)
  le() { printf "%d" "$((16#${b[$1+3]}${b[$1+2]}${b[$1+1]}${b[$1]}))"; }
  logs=1
  if (( ${#b[@]} != 1024 )) || [[ ${b[510]}${b[511]} != 55aa ]]; then logs=layout; fi
  if [[ $logs == 1 ]]; then
    for ((i = 478; i < 510; i++)); do [[ ${b[i]} == 00 ]] || logs=layout; done
    [[ ${b[*]:512:8} != "45 46 49 20 50 41 52 54" ]] || logs=layout
    for o in 446 462; do
      [[ ${b[o+4]} == 00 ]] && continue
      (( $(le $((o + 8))) + $(le $((o + 12))) <= off / 512 )) || logs=layout
    done
  fi
  if [[ $logs == 1 ]]; then
    hex() { printf "%02x %02x %02x %02x" $(($1 & 255)) $(($1 >> 8 & 255)) $(($1 >> 16 & 255)) $(($1 >> 24 & 255)); }
    want="00 fe ff ff 0e fe ff ff $(hex $((off / 512))) $(hex $((img_size / 512)))"
    entry=""
    for x in $want; do entry+="\\x$x"; done
    printf "%b" "$entry" | dd of="$fd" bs=16 count=1 oflag=seek_bytes seek=478 conv=notrunc,fsync status=none
    blockdev --flushbufs "$fd"
    got=$(dd if="$fd" bs=16 count=1 iflag=skip_bytes skip=478 status=none | od -An -v -tx1 | tr -s " \n" " ")
    got=${got# }
    [[ ${got% } == "$want" ]] || exit 21
    blockdev --rereadpt "$fd" 2>/dev/null || true
    logs=created
  fi
fi
echo "logs=$logs"
'

    say "" "Writing. This takes a few minutes; do not unplug the stick."
    local out rc=0
    out=$("${elev[@]}" bash --noprofile --norc -c "$root_code" make-stick-root \
        "${d_id[pick]}" "$(printf '%s' "${d_serial[pick]}" | od -An -v -tx1 | tr -d ' \n')" \
        "${d_size[pick]}" "$iso_size" "$iso_sha" "$img_sha" \
        < <(cat -- "$work/installer.iso" "$work/gjlogs.img")) || rc=$?

    if [[ $out != started* ]]; then
        log "GJA-3021 maker.elevation.declined"
        fail "No admin rights for the write. Nothing was written."
    fi
    [[ $out != *written* ]] || log "GJA-3030 maker.write.finished"
    case $rc in
    0) ;;
    11) fail "The stick changed after you picked it. Nothing was written. Run the maker again." ;;
    12) fail "The stick is in use (mounted, open in another program or used as swap). Close what uses it and run the maker again. Nothing was written." ;;
    13) fail "The stick is too small. The installer needs $(gb "$iso_size"). Nothing was written." ;;
    20) fail "More data reached the stick than was signed. The stick was wiped so it does not start." ;;
    21)
        log "GJA-3032 maker.readback.mismatch"
        fail "The stick did not read back what was written. Its start was wiped so it does not boot. Try another stick."
        ;;
    *) fail "The write failed (code $rc). The stick may not start; run the maker again." ;;
    esac
    [[ $out == *verified* ]] || fail "The write ended without a read-back check."
    log "GJA-3031 maker.readback.verified"
    case $out in
    *logs=created*) log "GJA-3040 maker.logs.created" ;;
    *logs=layout*)
        log "GJA-3041 maker.logs.skipped reason=layout"
        say "No GJLOGS partition: this image leaves no free slot for it. The installer offers other drives for its log."
        ;;
    *)
        log "GJA-3041 maker.logs.skipped reason=too-small"
        say "No GJLOGS partition: the stick has no room after the image. The installer offers other drives for its log."
        ;;
    esac
    say "" "The stick is ready: gjallarOS installer $iso_tag, read back and checked." \
        "Start the computer you want to install on from this stick."
    # Exit here: the EXIT trap needs this function's locals (work, log_file).
    exit 0
}

gjallar_make_stick "$@"
