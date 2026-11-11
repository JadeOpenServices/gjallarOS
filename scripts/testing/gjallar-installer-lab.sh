#!/usr/bin/env bash
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
state="${GJALLAR_VM_STATE:-$repo/.vm/gjallar-installer}"
disk="$state/gjallar-test.qcow2"
vars="$state/OVMF_VARS.fd"
tpm="$state/tpm"
tpm_sock="$tpm/swtpm.sock"
disk_size="${GJALLAR_VM_DISK_SIZE:-80G}"
memory="${GJALLAR_VM_MEMORY:-8192}"
cpus="${GJALLAR_VM_CPUS:-6}"

say() {
    printf '%s\n' "$*" >&2
}

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    return 1
}

need() {
    command -v "$1" >/dev/null 2>&1 ||
        fail "required command missing: $1"
}

resolve_iso() {
    local out iso

    say "BUILD: gjallar-installer-lab-iso"
    out="$(
        cd "$repo"
        nix build \
            --no-link \
            --print-out-paths \
            '.#gjallar-installer-lab-iso'
    )"

    iso="$(
        find "$out" -maxdepth 3 -type f -name '*.iso' -print -quit
    )"

    [[ -n "$iso" && -f "$iso" ]] ||
        fail "built installer-lab output contains no ISO"

    printf '%s\n' "$iso"
}

resolve_ovmf() {
    local out code template

    out="$(
        nix build \
            --no-link \
            --print-out-paths \
            'nixpkgs#OVMF.fd'
    )"

    code="$out/FV/OVMF_CODE.fd"
    template="$out/FV/OVMF_VARS.fd"

    [[ -f "$code" ]] ||
        fail "OVMF_CODE.fd not found at $code"

    [[ -f "$template" ]] ||
        fail "OVMF_VARS.fd not found at $template"

    printf '%s\n%s\n' "$code" "$template"
}

prepare_state() {
    local template="$1"

    mkdir -p "$state" "$tpm"

    if [[ ! -f "$disk" ]]; then
        say "CREATE: disposable virtual disk $disk ($disk_size)"
        qemu-img create -f qcow2 "$disk" "$disk_size"
    fi

    if [[ ! -f "$vars" ]]; then
        say "CREATE: private writable UEFI variable store"
        cp --reflink=auto "$template" "$vars"
        chmod u+w "$vars"
    fi
}

stop_tpm() {
    if [[ -f "$tpm/pid" ]]; then
        local pid
        pid="$(cat "$tpm/pid" 2>/dev/null || true)"
        if [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" || true
            wait "$pid" 2>/dev/null || true
        fi
        rm -f "$tpm/pid"
    fi
    rm -f "$tpm_sock"
}

start_tpm() {
    stop_tpm

    mkdir -p "$tpm"

    swtpm socket \
        --tpm2 \
        --tpmstate "dir=$tpm" \
        --ctrl "type=unixio,path=$tpm_sock" \
        --daemon \
        --pid "file=$tpm/pid"

    [[ -S "$tpm_sock" ]] ||
        fail "swtpm socket was not created"
}

qemu_common() {
    local code="$1"

    printf '%s\0' \
        qemu-system-x86_64 \
        -name gjallar-installer-test \
        -machine q35,accel=kvm \
        -enable-kvm \
        -cpu host \
        -smp "$cpus" \
        -m "$memory" \
        -drive "if=pflash,format=raw,readonly=on,file=$code" \
        -drive "if=pflash,format=raw,file=$vars" \
        -drive "if=none,id=systemdisk,file=$disk,format=qcow2,cache=writeback,discard=unmap" \
        -device virtio-blk-pci,drive=systemdisk,serial=GJALLAR-VM-TARGET-001 \
        -device virtio-net-pci,netdev=net0 \
        -netdev user,id=net0 \
        -chardev "socket,id=chrtpm,path=$tpm_sock" \
        -tpmdev emulator,id=tpm0,chardev=chrtpm \
        -device tpm-tis,tpmdev=tpm0 \
        -device virtio-rng-pci \
        -device qemu-xhci \
        -device usb-tablet \
        -boot menu=on

    if [[ "${GJALLAR_VM_DISPLAY:-serial}" = serial ]]; then
        printf '%s\0' \
            -display none \
            -serial mon:stdio
    else
        printf '%s\0' \
            -display gtk
    fi
}

run_install() {
    local iso="$1"
    local code="$2"
    local -a cmd=()

    start_tpm
    trap stop_tpm EXIT INT TERM

    while IFS= read -r -d '' arg; do
        cmd+=("$arg")
    done < <(qemu_common "$code")

    cmd+=(
        -drive "media=cdrom,readonly=on,file=$iso"
        -boot order=d,menu=on
    )

    say
    say "=== GJALLAR INSTALLER LAB ==="
    say "Virtual target disk inside guest: /dev/vda"
    say "Physical host disks: NOT attached"
    say "Installer repository inside guest: /run/gjallarOS/repo"
    say
    say "Inside the gjallarOS environment, run the installer against:"
    say "  /dev/vda"
    say
    say "When asked for destructive confirmation, it should require:"
    say '  ERASE /dev/vda'
    say
    say "After installation finishes, shut the VM down normally."
    say

    "${cmd[@]}"
}

run_boot() {
    local code="$1"
    local -a cmd=()

    [[ -f "$disk" ]] ||
        fail "VM disk does not exist; run install first"

    start_tpm
    trap stop_tpm EXIT INT TERM

    while IFS= read -r -d '' arg; do
        cmd+=("$arg")
    done < <(qemu_common "$code")

    cmd+=(-boot order=c,menu=on)

    say
    say "=== VM INSTALLED-DISK BOOT ==="
    say "Installer ISO is NOT attached."
    say "The VM must boot exclusively from the installed qcow2."
    say

    "${cmd[@]}"
}

inspect_disk() {
    [[ -f "$disk" ]] ||
        fail "VM disk does not exist"

    say "=== QCOW2 ==="
    qemu-img info "$disk"

    say
    say "NOTE:"
    say "Guest GPT/LUKS/Btrfs inspection is intentionally performed"
    say "inside the VM, not by attaching the qcow2 to the host."
}

reset_vm() {
    stop_tpm
    rm -rf "$state"
    say "PASS: disposable VM state removed: $state"
}

preflight() {
    need nix
    need qemu-system-x86_64
    need qemu-img
    need swtpm

    [[ -e /dev/kvm ]] ||
        fail "/dev/kvm is missing"
    [[ -r /dev/kvm && -w /dev/kvm ]] ||
        fail "current user cannot access /dev/kvm"

    say "PASS: QEMU present"
    say "PASS: KVM accessible"
    say "PASS: swtpm present"
    say "PASS: harness never passes a host block device to QEMU"
}

main() {
    local action="${1:-help}"
    local ovmf code template iso

    case "$action" in
        preflight)
            preflight
            ;;

        install)
            preflight
            ovmf="$(resolve_ovmf)"
            code="$(printf '%s\n' "$ovmf" | sed -n '1p')"
            template="$(printf '%s\n' "$ovmf" | sed -n '2p')"
            prepare_state "$template"
            iso="$(resolve_iso)"
            run_install "$iso" "$code"
            ;;

        boot)
            preflight
            ovmf="$(resolve_ovmf)"
            code="$(printf '%s\n' "$ovmf" | sed -n '1p')"
            template="$(printf '%s\n' "$ovmf" | sed -n '2p')"
            prepare_state "$template"
            run_boot "$code"
            ;;

        inspect)
            inspect_disk
            ;;

        reset)
            reset_vm
            ;;

        *)
            cat <<EOF
Usage:
  $0 preflight
  $0 install
  $0 boot
  $0 inspect
  $0 reset

install
  Builds the real gjallarOS recovery/installer ISO, creates an
  80 GiB disposable qcow2 disk, starts QEMU/KVM with UEFI + TPM2,
  and boots the ISO.

boot
  Starts the same VM WITHOUT the ISO. This proves whether the
  installed virtual disk is independently bootable.

inspect
  Shows qcow2 metadata only. It does not attach the virtual disk
  to the host.

reset
  Deletes only:
    $state

Environment overrides:
  GJALLAR_VM_STATE
  GJALLAR_VM_DISK_SIZE
  GJALLAR_VM_MEMORY
  GJALLAR_VM_CPUS
  GJALLAR_VM_DISPLAY   serial (default) or gtk
EOF
            ;;
    esac
}

main "$@"
