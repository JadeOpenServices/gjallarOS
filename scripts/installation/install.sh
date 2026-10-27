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

# Started outside a checkout (curl -fsSL .../install.sh | bash): clone the
# repository with its submodules and run the installer from the clone. The
# pipe is the script itself, so the installer reads answers from the terminal.
script_file="${BASH_SOURCE[0]:-}"
if [[ ! -f "$script_file" || ! -f "$(dirname "$script_file")/../../flake.nix" ]]; then
    repo_url="${GJALLAR_REPO_URL:-https://github.com/JadeOpenServices/gjallarOS.git}"
    checkout="${GJALLAR_DIR:-$HOME/Documents/gjallarOS}"
    git_cmd=(git)
    # No checkout yet to take nixpkgs from: use the revision flake.lock pins,
    # checked against its hash (tests/nix/bootstrap-pins.nix keeps them equal).
    command -v git >/dev/null 2>&1 ||
        git_cmd=(nix shell 'github:nixos/nixpkgs/8ce73368d1efd4d657e9064d29b144a52046b4e8?narHash=sha256-F7meJtbqGttmUoMmYjRRgkVs42sn7bh7vvRvLPlUN98%3D#git' --command git)
    if [[ -e "$checkout/.git" ]]; then
        printf 'Using the existing checkout in %s\n' "$checkout"
        "${git_cmd[@]}" -C "$checkout" submodule update --init --recursive
    else
        [[ ! -e "$checkout" ]] || {
            printf 'ERROR: %s exists and is not a git checkout. Set GJALLAR_DIR to another directory.\n' "$checkout" >&2
            exit 1
        }
        printf 'Cloning %s into %s...\n' "$repo_url" "$checkout"
        mkdir -p "$(dirname "$checkout")"
        "${git_cmd[@]}" clone --recurse-submodules "$repo_url" "$checkout"
    fi
    [[ "${GJALLAR_CLONE_ONLY:-0}" == 1 ]] && exit 0
    [[ -r /dev/tty ]] || {
        printf 'ERROR: the installer is interactive and needs a terminal.\n' >&2
        exit 1
    }
    exec bash "$checkout/scripts/installation/install.sh" "$@" </dev/tty
fi

script_dir="$(cd "$(dirname "$script_file")" && pwd)"
repo="$(cd "$script_dir/../.." && pwd)"

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
    git_cmd=(git)
    if ! command -v git >/dev/null 2>&1; then
        git_cmd=(nix --extra-experimental-features 'nix-command flakes' shell
            --inputs-from "path:$flake_src" nixpkgs#git --command git)
    fi
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
