#!/usr/bin/env python3
"""Offer explicit exFAT setup for unformatted, unmounted USB disks."""
import html
import json
from pathlib import Path
import subprocess
import time

SERVICE = 'org.freedesktop.UDisks2'
BASE = '/org/freedesktop/UDisks2'


def run(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()


def disks():
    return json.loads(run('lsblk', '--json', '--bytes', '--output',
        'NAME,PATH,TYPE,TRAN,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MODEL,SERIAL'))['blockdevices']


def eligible(disk):
    return (disk.get('type') == 'disk' and disk.get('tran') == 'usb'
        and int(disk.get('size') or 0) > 16 * 1024 * 1024
        and not disk.get('ro') and not disk.get('fstype') and not disk.get('pttype')
        and not disk.get('children') and not any(disk.get('mountpoints') or []))


def identity(disk):
    sysfs = Path('/sys/class/block') / disk['name']
    if any((sysfs / 'holders').iterdir()):
        raise RuntimeError('Disk is in use by another block device.')
    return (disk['path'], disk.get('serial'), disk['size'], (sysfs / 'diskseq').read_text().strip())


def revalidate(disk, token, *, blank):
    current = next((d for d in disks() if d['path'] == disk['path']), None)
    if current is None or identity(current) != token:
        raise RuntimeError('Disk changed or disconnected. Reconnect it and review again.')
    if blank and not eligible(current):
        raise RuntimeError('Disk is no longer empty of recognized filesystems. Setup cancelled.')
    return current


def call(obj, interface, method, signature, *args):
    output = run('busctl', '--system', '--json=short', '--timeout=120',
        '--allow-interactive-authorization=yes', 'call', SERVICE, obj,
        SERVICE + '.' + interface, method, signature, *map(str, args))
    return json.loads(output)['data'] if output else []


def setup(disk, token):
    # Capability and identity checks precede every destructive step. UDisks
    # retains its normal Polkit authorization; this daemon never runs as root.
    supported = call(BASE + '/Manager', 'Manager', 'CanFormat', 's', 'exfat')
    if not supported[0][0]:
        raise RuntimeError('exFAT formatting support is unavailable.')
    revalidate(disk, token, blank=True)
    obj = BASE + '/block_devices/' + disk['name']
    call(obj, 'Block', 'Format', 'sa{sv}', 'gpt', 0)
    run('udevadm', 'settle', '--timeout=10')
    current = revalidate(disk, token, blank=False)
    if current.get('children') or current.get('pttype') != 'gpt':
        raise RuntimeError('Unexpected partition layout after initialization; stopped.')
    partition = call(obj, 'PartitionTable', 'CreatePartitionAndFormat',
        'ttssa{sv}sa{sv}', 1048576, 0, '', 'External', 0, 'exfat',
        1, 'label', 's', 'External')[0]
    revalidate(disk, token, blank=False)
    try:
        # Disks from another system open read-only by default (Plane 286).
        # This one was just formatted here on the user's request, so it
        # opens writable.
        call(partition, 'Filesystem', 'Mount', 'a{sv}', 1, 'options', 's', 'rw')
    except subprocess.CalledProcessError as error:
        # udiskie may have mounted the new filesystem first.
        if 'org.freedesktop.UDisks2.Error.AlreadyMounted' not in (error.stderr or ''):
            raise


def confirm(disk):
    text = (f"{disk.get('model') or 'USB disk'} — {disk['size'] / 1024**3:.1f} GiB\n"
        f"Device: {disk['path']}\nSerial: {disk.get('serial') or 'unavailable'}\n\n"
        'No partition table or filesystem was recognized. This does not prove the disk contains no data.\n\n'
        'Recommended: one exFAT partition for portable storage.\n'
        'Formatting erases all data on this disk. Continue only if it is empty or backed up.')
    return subprocess.run(['zenity', '--question', '--default-cancel',
        '--title=Set up USB disk', '--width=540', '--ok-label=Format as exFAT',
        '--cancel-label=Leave unchanged', '--text=' + html.escape(text)],
        check=False).returncode == 0


def main():
    seen = set()
    while True:
        active = set()
        try:
            for disk in disks():
                if not eligible(disk):
                    continue
                token = identity(disk)
                active.add(token)
                if token in seen:
                    continue
                seen.add(token)
                if confirm(disk):
                    try:
                        setup(disk, token)
                    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
                        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
                        subprocess.run(['zenity', '--error', '--title=USB disk setup stopped',
                            '--text=' + html.escape(str(detail))], check=False)
            seen.intersection_update(active)
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            print(f'USB disk review: {error}', flush=True)
        time.sleep(5)


if __name__ == '__main__':
    main()
