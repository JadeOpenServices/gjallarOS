#!/usr/bin/env python3
"""Integration regression test for the pinned OpenVFS hydration patch.

Run as the desktop user with access to /dev/fuse, cat and fusermount3:
    python3 tests/openvfs-hydration.py /path/to/openvfs [--timeout]

Uses only a fresh temporary mount and a fake local sync socket. The optional
five-minute timeout check and the slow download next to it (about six minutes
together) require no server or Nextcloud account. Logs remain
in the printed temporary directory. Never mounts the real Nextcloud directory.
"""
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
if len(sys.argv) < 2:
    sys.exit('Usage: openvfs-hydration.py /path/to/openvfs [--timeout]')

base = Path(tempfile.mkdtemp(prefix='gjallar-vfs-test-'))
mount = base / 'mount'
mount.mkdir()

def pack(x):
    # Minimal MessagePack encoding for the small fixture attribute maps.
    if isinstance(x, str):
        b = x.encode()
        return bytes([160 + len(b)]) + b
    if isinstance(x, int):
        return bytes([x])
    return bytes([128 + len(x)]) + b''.join((pack(k) + pack(v) for k, v in x.items()))

def attrs(name, state):
    return pack(dict(etag='test', fileid=name, size=0 if state == 0 else 7, state=state, pinstate=1))
os.setxattr(mount, 'user.openvfs.registration', pack(dict(owner='test', version=0)))
for name in ('duplicate', 'inflight', 'preview', 'error', 'cancel', 'timeout', 'copy', 'real-kio', 'gone', 'slow'):
    p = mount / name
    p.touch()
    os.setxattr(p, 'user.openvfs.data', attrs(name, 2 if name == 'inflight' else 1))
config = base / 'config.json'
config.write_text(json.dumps({'ignoreApps': {'byName': [], 'endsWith': ['dolphin', 'kioworker']}}))
sock = socket.socket(socket.AF_UNIX)
sock.bind(str(base / 'socket'))
sock.listen()
requests = []
errors = []
conn = None
pending = {}

def slow_download(req):
    # One byte every 50 s: the whole file takes longer than the deadline, but
    # no gap between two writes reaches it.
    try:
        f = os.open(mount / 'slow', os.O_WRONLY)
        for b in b'fixture':
            os.write(f, bytes([b]))
            time.sleep(50)
        os.setxattr(f, 'user.openvfs.data', attrs('slow', 0))
        os.close(f)
        reply = {'id': req['id'], 'arguments': {'status': 'OK'}}
        conn.sendall(b'V2/HYDRATE_FILE_RESULT:' + json.dumps(reply).encode() + b'\n')
    except Exception as e:
        errors.append(repr(e))

def server():
    global conn
    try:
        conn, _ = sock.accept()
        buf = b''
        while True:
            data = conn.recv(4096)
            if not data:
                return
            buf += data
            while b'\n' in buf:
                line, buf = buf.split(b'\n', 1)
                if line.startswith(b'VERSION'):
                    conn.sendall(b'VERSION:34.0.50:1.1\n')
                    continue
                if not line.startswith(b'V2/HYDRATE_FILE:'):
                    continue
                req = json.loads(line.split(b':', 1)[1])
                name = Path(req['arguments']['file']).name
                requests.append((name, req['id']))
                if name in ('cancel', 'timeout'):
                    continue
                if name == 'slow':
                    threading.Thread(target=slow_download, args=(req,), daemon=True).start()
                    continue
                if name == 'gone':
                    # The sync client quits with a hydration still open.
                    conn.shutdown(socket.SHUT_RDWR)
                    return
                pending.setdefault(name, []).append(req)
                if name == 'duplicate' and len(pending[name]) < 3:
                    continue
                if name != 'error':
                    # Write through FUSE, like Nextcloud, from a non-main thread.
                    f = os.open(mount / name, os.O_WRONLY)
                    os.write(f, b'fixture')
                    os.setxattr(f, 'user.openvfs.data', attrs(name, 0))
                    os.close(f)
                for r in pending.pop(name):
                    reply = {'id': r['id'], 'arguments': {'status': 'ERROR' if name == 'error' else 'OK'}}
                    conn.sendall(b'V2/HYDRATE_FILE_RESULT:' + json.dumps(reply).encode() + b'\n')
    except Exception as e:
        errors.append(repr(e))
threading.Thread(target=server, daemon=True).start()
log = open(base / 'daemon.log', 'w')
daemon = subprocess.Popen([sys.argv[1], '-f', '-i', str(config), '-o', 'test', '-s', str(base / 'socket'), str(mount)], stdout=log, stderr=log)
children = []
daemon2 = None
mount2 = base / 'mount2'

def cat(name, exe='cat'):
    p = subprocess.Popen(['cat', str(mount / name)], executable=exe, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    children.append(p)
    return p
try:
    for _ in range(100):
        if os.path.ismount(mount):
            break
        if daemon.poll() is not None:
            raise RuntimeError('mount failed')
        time.sleep(0.1)
    assert os.path.ismount(mount)
    readers = [cat('duplicate') for _ in range(3)]
    for p in readers:
        assert p.communicate(timeout=10)[0] == b'fixture' and p.returncode == 0
    assert len({i for n, i in requests if n == 'duplicate'}) == 3
    print('PASS: concurrent opens complete; sync-client threads bypass hydration without a VERSION PID', flush=True)
    p = cat('inflight')
    assert p.communicate(timeout=10)[0] == b'fixture' and p.returncode == 0
    print('PASS: already-hydrating file waits for data', flush=True)
    wrapped = base / '.kioworker-wrapped'
    shutil.copyfile(shutil.which('cat'), wrapped)
    wrapped.chmod(493)
    p = cat('preview', str(wrapped))
    p.communicate(timeout=3)
    assert p.returncode != 0 and (not any((n == 'preview' for n, i in requests)))
    print('PASS: wrapped KIO preview does not trigger hydration', flush=True)
    module = base / 'kf6/kio/kio_file.so'
    module.parent.mkdir(parents=True)
    module.touch()
    (base / 'file').touch()
    p = subprocess.Popen(['cat', str(module), 'file', str(mount / 'copy')], executable=str(wrapped), cwd=base,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    children.append(p)
    assert p.communicate(timeout=10)[0] == b'fixture' and p.returncode == 0
    print('PASS: wrapped KIO file worker can hydrate for a real copy', flush=True)
    kio_copy = next((arg.split('=', 1)[1] for arg in sys.argv[2:] if arg.startswith('--kio-copy=')), None)
    if kio_copy:
        source = str(mount / 'real-kio')
        blocked = subprocess.run([kio_copy, source, str(base / 'threaded-copy')],
            env=os.environ | {'KIO_ENABLE_WORKER_THREADS': '1'}, capture_output=True, timeout=15)
        assert blocked.returncode == 1 and not any(n == 'real-kio' for n, _ in requests)
        copied = subprocess.run([kio_copy, source, str(base / 'separate-copy')],
            env=os.environ | {'KIO_ENABLE_WORKER_THREADS': '0'}, capture_output=True, timeout=15)
        assert copied.returncode == 0, copied.stderr.decode()
        assert (base / 'separate-copy').read_bytes() == b'fixture'
        print('PASS: real KIO reproduces threaded read denial and copies successfully with separate workers', flush=True)
    p = cat('error')
    p.communicate(timeout=5)
    assert p.returncode != 0
    print('PASS: server error releases reader', flush=True)
    p = cat('cancel')
    for _ in range(50):
        if any((n == 'cancel' for n, i in requests)):
            break
        time.sleep(0.1)
    assert any((n == 'cancel' for n, i in requests))
    p.send_signal(signal.SIGINT)
    p.communicate(timeout=5)
    print('PASS: cancelled reader exits without server reply', flush=True)
    waiting = Path('/sys/fs/fuse/connections') / str(os.minor(os.stat(mount).st_dev)) / 'waiting'
    for _ in range(30):
        if int(waiting.read_text()) == 0:
            break
        time.sleep(0.1)
    assert int(waiting.read_text()) == 0, 'FUSE requests remain blocked after cancellation'
    if '--timeout' in sys.argv[2:]:
        start = time.monotonic()
        p = cat('timeout')
        slow = cat('slow')
        p.communicate(timeout=310)
        assert p.returncode != 0 and 295 <= time.monotonic() - start < 310
        print('PASS: unanswered request expires after five minutes', flush=True)
        assert slow.communicate(timeout=90)[0] == b'fixture' and slow.returncode == 0
        assert time.monotonic() - start > 340
        print('PASS: slow download that outlasts the deadline still completes', flush=True)
    # A second mount whose client never answers: SIGTERM must end openvfs
    # and unmount while a reader still waits for hydration.
    mount2.mkdir()
    os.setxattr(mount2, 'user.openvfs.registration', pack(dict(owner='test', version=0)))
    (mount2 / 'stuck').touch()
    os.setxattr(mount2 / 'stuck', 'user.openvfs.data', attrs('stuck', 1))
    sock2 = socket.socket(socket.AF_UNIX)
    sock2.bind(str(base / 'socket2'))
    sock2.listen()
    stuck = []

    def silent_server():
        c, _ = sock2.accept()
        try:
            while (data := c.recv(4096)):
                if data.startswith(b'VERSION'):
                    c.sendall(b'VERSION:34.0.50:1.1\n')
                stuck.extend(line for line in data.split(b'\n') if line.startswith(b'V2/HYDRATE_FILE:'))
        except ConnectionResetError:
            pass  # openvfs exited
    threading.Thread(target=silent_server, daemon=True).start()
    daemon2 = subprocess.Popen([sys.argv[1], '-f', '-i', str(config), '-o', 'test', '-s', str(base / 'socket2'), str(mount2)],
                               stdout=log, stderr=log)
    for _ in range(100):
        if os.path.ismount(mount2):
            break
        time.sleep(0.1)
    assert os.path.ismount(mount2)
    p = subprocess.Popen(['cat', str(mount2 / 'stuck')], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    children.append(p)
    for _ in range(50):
        if stuck:
            break
        time.sleep(0.1)
    assert stuck
    start = time.monotonic()
    daemon2.send_signal(signal.SIGTERM)
    try:
        daemon2.wait(timeout=5)
    except subprocess.TimeoutExpired:
        raise AssertionError('openvfs ignored SIGTERM during hydration') from None
    p.communicate(timeout=5)
    assert p.returncode != 0 and not os.path.ismount(mount2)
    print(f'PASS: SIGTERM during hydration exits and unmounts in {time.monotonic() - start:.1f} s', flush=True)
    # Last case: it ends the fake client. The waiter and later opens fail fast.
    start = time.monotonic()
    p = cat('gone')
    p.communicate(timeout=5)
    assert p.returncode != 0
    p = cat('gone')
    p.communicate(timeout=5)
    assert p.returncode != 0 and time.monotonic() - start < 5
    print('PASS: sync client exit releases the waiter and later opens fail fast', flush=True)
    assert not errors, errors
finally:
    subprocess.run(['fusermount3', '-uz', str(mount)], capture_output=True, timeout=5)
    if daemon2 is not None:
        # A hung daemon also hangs stat() on its mount, so kill it first.
        if daemon2.poll() is None:
            daemon2.kill()
            daemon2.wait()
        subprocess.run(['fusermount3', '-uz', str(mount2)], capture_output=True, timeout=5)
    daemon.terminate()
    try:
        daemon.wait(timeout=3)
    except subprocess.TimeoutExpired:
        daemon.kill()
        daemon.wait()
    for p in children:
        if p.poll() is None:
            p.kill()
        p.wait(timeout=5)
    sock.close()
    if conn is not None:
        conn.close()
    print('Fixture log:', base / 'daemon.log', flush=True)
