#!/usr/bin/env bash

# Enable the modern Nix CLI and flakes for the entire installer bootstrap.
if [ -n "${NIX_CONFIG:-}" ]; then
    NIX_CONFIG="${NIX_CONFIG}
extra-experimental-features = nix-command flakes"
    export NIX_CONFIG
else
    export NIX_CONFIG='extra-experimental-features = nix-command flakes'
fi

# Build and launch the installer without changing the caller's environment.
set -euo pipefail

command -v nix >/dev/null 2>&1 || {
    printf 'ERROR: Nix is required to bootstrap GjallarOS.\n' >&2
    exit 1
}

# GjallarOS boots with systemd-boot or lanzaboote from an ESP; a machine
# started in legacy BIOS mode would install and then not boot.
[[ -d /sys/firmware/efi ]] || {
    printf 'ERROR: GjallarOS needs UEFI boot. This system was started in legacy BIOS mode; switch the firmware to UEFI (disable CSM) and boot the installer media again.\n' >&2
    exit 1
}

git_cmd=(git)
# Before the checkout is staged there is no flake.lock to take nixpkgs from:
# use the revision flake.lock pins, checked against its hash
# (tests/nix/bootstrap-pins.nix keeps them equal).
command -v git >/dev/null 2>&1 ||
    git_cmd=(nix shell 'github:nixos/nixpkgs/8ce73368d1efd4d657e9064d29b144a52046b4e8?narHash=sha256-F7meJtbqGttmUoMmYjRRgkVs42sn7bh7vvRvLPlUN98%3D#git' --command git)

# Release channel: --channel dev|staging|stable or GJALLAR_CHANNEL picks the
# branch the checkout follows, and the installer records the same channel.
# Without either, a new clone follows the default branch (stable) and an
# existing checkout stays on the branch it has.
channel="${GJALLAR_CHANNEL:-}"
channel_flag=""
expect_channel=0
for arg in "$@"; do
    if (( expect_channel )); then
        channel_flag="$arg"
        expect_channel=0
        continue
    fi
    case "$arg" in
        --channel=*) channel_flag="${arg#--channel=}" ;;
        --channel) expect_channel=1 ;;
    esac
done
(( ! expect_channel )) || {
    printf 'ERROR: --channel needs a value: dev, staging or stable.\n' >&2
    exit 1
}
if [[ -n "$channel_flag" ]]; then
    [[ -z "$channel" || "$channel" == "$channel_flag" ]] || {
        printf 'ERROR: GJALLAR_CHANNEL=%s and --channel %s disagree. Use one of them.\n' "$channel" "$channel_flag" >&2
        exit 1
    }
    channel="$channel_flag"
elif [[ -n "$channel" ]]; then
    set -- "$@" --channel "$channel"
fi
case "$channel" in
    "" | dev | staging | stable) ;;
    *)
        printf 'ERROR: unknown release channel %s. Use dev, staging or stable.\n' "$channel" >&2
        exit 1
        ;;
esac

# An installed GjallarOS that builds from the root-owned system checkout
# runs the installer there, as root (wiki 752 Q214): settings written to any
# other checkout are never built. gjallar-system-rebuild --update moves the
# system checkout to the chosen channel, under the same checks as
# gjallarctl rebuild, so this script leaves its branch alone. The installer
# refuses any other checkout on such a host by itself; this only takes the
# user there. GJALLAR_SYSTEM_REPOSITORY_FILE is for the bootstrap tests.
system_checkout=/etc/gjallar/system
system_repository_file="${GJALLAR_SYSTEM_REPOSITORY_FILE:-/etc/gjallar/repository}"
system_install=0
if [[ -r "$system_repository_file" ]]; then
    system_repository="$(head -n 1 "$system_repository_file")"
    [[ "${system_repository%/}" != "$system_checkout" ]] || system_install=1
fi
if (( system_install )); then
    [[ -f "$system_checkout/scripts/installation/install.sh" ]] || {
        printf 'ERROR: this GjallarOS builds from %s, but that checkout is missing; nothing was changed.\n' "$system_checkout" >&2
        exit 1
    }
    self="$(realpath -e -- "${BASH_SOURCE[0]:-/dev/stdin}" 2>/dev/null || true)"
    if (( EUID != 0 )) || [[ "$self" != "$system_checkout/scripts/installation/install.sh" ]]; then
        printf 'This GjallarOS builds from %s; the installer runs there as root.\n' "$system_checkout"
        (exec </dev/tty) 2>/dev/null || {
            printf 'ERROR: the installer is interactive and needs a terminal.\n' >&2
            exit 1
        }
        if (( EUID == 0 )); then
            exec bash "$system_checkout/scripts/installation/install.sh" "$@" </dev/tty
        fi
        exec sudo bash "$system_checkout/scripts/installation/install.sh" "$@" </dev/tty
    fi
fi

# Moves a checkout onto the channel branch. Only a fast-forward: local
# commits or edits to tracked files stop the install instead of being lost.
sync_channel() {
    local dir="$1"
    [[ -n "$channel" ]] || return 0
    if [[ -n "$("${git_cmd[@]}" -C "$dir" status --porcelain --untracked-files=no)" ]]; then
        printf 'ERROR: %s has uncommitted changes to tracked files. Commit or stash them, then run the installer again.\n' "$dir" >&2
        exit 1
    fi
    printf 'Updating %s to the %s channel...\n' "$dir" "$channel"
    "${git_cmd[@]}" -C "$dir" fetch origin "+refs/heads/$channel:refs/remotes/origin/$channel"
    if "${git_cmd[@]}" -C "$dir" rev-parse --verify --quiet "refs/heads/$channel" >/dev/null; then
        "${git_cmd[@]}" -C "$dir" switch --quiet "$channel"
        "${git_cmd[@]}" -C "$dir" merge --ff-only --quiet "origin/$channel" || {
            printf 'ERROR: the local %s branch in %s has commits that origin/%s does not have. Move them to another branch, then run the installer again.\n' "$channel" "$dir" "$channel" >&2
            exit 1
        }
    else
        "${git_cmd[@]}" -C "$dir" switch --quiet --create "$channel" --track "origin/$channel"
    fi
    "${git_cmd[@]}" -C "$dir" submodule update --init --recursive
}

# Started outside a checkout (curl -fsSL .../install.sh | bash): clone the
# repository with its submodules and run the installer from the clone. The
# pipe is the script itself, so the installer reads answers from the terminal.
script_file="${BASH_SOURCE[0]:-}"
if [[ ! -f "$script_file" || ! -f "$(dirname "$script_file")/../../flake.nix" ]]; then
    repo_url="${GJALLAR_REPO_URL:-https://github.com/JadeOpenServices/gjallarOS.git}"
    checkout="${GJALLAR_DIR:-$HOME/Documents/gjallarOS}"
    if [[ -e "$checkout/.git" ]]; then
        printf 'Using the existing checkout in %s\n' "$checkout"
        if [[ -n "$channel" ]]; then
            sync_channel "$checkout"
        else
            "${git_cmd[@]}" -C "$checkout" submodule update --init --recursive
        fi
    else
        [[ ! -e "$checkout" ]] || {
            printf 'ERROR: %s exists and is not a git checkout. Set GJALLAR_DIR to another directory.\n' "$checkout" >&2
            exit 1
        }
        printf 'Cloning %s into %s...\n' "$repo_url" "$checkout"
        mkdir -p "$(dirname "$checkout")"
        branch_args=()
        [[ -z "$channel" ]] || branch_args=(--branch "$channel")
        "${git_cmd[@]}" clone --recurse-submodules "${branch_args[@]}" "$repo_url" "$checkout"
    fi
    [[ "${GJALLAR_CLONE_ONLY:-0}" == 1 ]] && exit 0
    [[ -r /dev/tty ]] || {
        printf 'ERROR: the installer is interactive and needs a terminal.\n' >&2
        exit 1
    }
    GJALLAR_CHANNEL_SYNCED=1 exec bash "$checkout/scripts/installation/install.sh" "$@" </dev/tty
fi

script_dir="$(cd "$(dirname "$script_file")" && pwd)"
repo="$(cd "$script_dir/../.." && pwd)"

# Run from a checkout with a channel: move it to that branch first, then run
# the script from the updated tree, since the branch may change this script
# and the installer.
if [[ -n "$channel" && "${GJALLAR_CHANNEL_SYNCED:-0}" != 1 ]] && (( ! system_install )); then
    sync_channel "$repo"
    GJALLAR_CHANNEL_SYNCED=1 exec bash "$repo/scripts/installation/install.sh" "$@"
fi

# Nix evaluates a staged copy of the checkout: a path: reference to the
# checkout itself copies .git (gigabytes of history) and ignored scratch such
# as .vm into the store, which is RAM on live media. Same exclusions as
# internal/installer/flakesource.
flake_tmp="$(mktemp -d "${TMPDIR:-/tmp}/gjallar-flake-XXXXXX")"
trap 'rm -rf "$flake_tmp"' EXIT
flake_src="$flake_tmp/source"
stage_flake() {
    rm -rf "$flake_src"
    mkdir "$flake_src"
    tar -C "$repo" --exclude=.git --anchored --exclude=./.vm --exclude=./.claude \
        --exclude=./.direnv --exclude=./result --exclude='./result-*' -cf - . |
        tar -C "$flake_src" -xf -
}
stage_flake

# A plain git clone leaves submodules empty. Resolve required source before
# building anything or applying prerequisite changes to the installed system.
if [[ ! -f "$repo/pkgs/monique/nix/nixos-module.nix" ]]; then
    printf 'Preparing the required Monique submodule...\n'
    if ! "${git_cmd[@]}" -C "$repo" submodule update --init --recursive -- pkgs/monique ||
        [[ ! -f "$repo/pkgs/monique/nix/nixos-module.nix" ]]; then
        printf 'ERROR: Monique source is missing. Use git clone --recurse-submodules or copy the complete repository including pkgs/monique.\n' >&2
        exit 1
    fi
    stage_flake
fi

printf 'Building the GjallarOS installer...\n'
installer_path="$(nix --extra-experimental-features 'nix-command flakes' build \
    --no-link --print-out-paths "path:$flake_src#gjallar-installer")"

probe_tools=(
    nixpkgs#pciutils
    nixpkgs#usbutils
    nixpkgs#util-linux
    nixpkgs#libinput
    nixpkgs#iio-sensor-proxy
    nixpkgs#fprintd
    nixpkgs#bolt
    nixpkgs#alsa-utils
    nixpkgs#pipewire
    nixpkgs#fwupd
    nixpkgs#ethtool
    nixpkgs#iw
    # Provided by the prerequisite rebuild on installed hosts; live media
    # skips that rebuild and the minimal ISO ships none of them.
    nixpkgs#sbctl
    # Queues the GjallarOS key for shim on locked firmware (Plane 293).
    nixpkgs#mokutil
    nixpkgs#openssl
    nixpkgs#age
    nixpkgs#sops
)
# Questions open as GTK dialogs when a desktop session is running. Before the
# first rebuild the system has no zenity, so bring it along. A sudo run asks
# in the terminal (prompt.go sudoFromDesktop).
if [[ -n "${WAYLAND_DISPLAY:-}${DISPLAY:-}" && -z "${SSH_CONNECTION:-}" &&
    ! ( $EUID -eq 0 && -n "${SUDO_USER:-}" ) ]]; then
    probe_tools+=(nixpkgs#zenity)
fi

printf 'Preparing GjallarOS hardware probe environment...\n'

# Not exec: the EXIT trap removes the staged source afterwards.
nix --extra-experimental-features 'nix-command flakes' shell \
    --inputs-from "path:$flake_src" \
    "${probe_tools[@]}" \
    --command "$installer_path/bin/gjallar-installer" --repo "$repo" "$@"
