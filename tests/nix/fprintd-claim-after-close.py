"""Claim the reader again while fprintd still closes it for a client that left.

Connection A claims the libfprint virtual reader, asks it to take 3 s for the
next close, and disconnects without Release, as pam_fprintd does after a match.
Connection B waits until the bus reports A gone, which is when fprintd starts
the close, and claims. Prints one line: "claimed <seconds>" or
"refused <D-Bus error name>".
"""

import socket
import sys
import time

from gi.repository import Gio, GLib

SOCKET = sys.argv[1]
NAME = "net.reactivated.Fprint"
DEVICE = NAME + ".Device"
ADDRESS = Gio.dbus_address_get_for_bus_sync(Gio.BusType.SYSTEM, None)


FLAGS = Gio.DBusConnectionFlags
BUS_CLIENT = FLAGS.AUTHENTICATION_CLIENT | FLAGS.MESSAGE_BUS_CONNECTION


def connect():
    return Gio.DBusConnection.new_for_address_sync(ADDRESS, BUS_CLIENT,
                                                   None, None)


def call(conn, path, iface, method, args, timeout_ms=10000):
    return conn.call_sync(NAME, path, iface, method, args, None,
                          Gio.DBusCallFlags.NONE, timeout_ms, None)


a = connect()
b = connect()
device = call(a, "/net/reactivated/Fprint/Manager", NAME + ".Manager",
              "GetDefaultDevice", None).unpack()[0]
call(a, device, DEVICE, "Claim", GLib.Variant("(s)", ("",)))

# The virtual reader runs its command socket while it is open. It reads one
# command per connection and closes the connection once it has queued it, so
# EOF means the sleep is queued for the next close.
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(SOCKET)
s.sendall(b"SLEEP 3000")
s.shutdown(socket.SHUT_WR)
while s.recv(64):
    pass
s.close()

gone = []
a_name = a.get_unique_name()
b.signal_subscribe("org.freedesktop.DBus", "org.freedesktop.DBus",
                   "NameOwnerChanged", "/org/freedesktop/DBus", a_name,
                   Gio.DBusSignalFlags.NONE,
                   lambda *args: gone.append(args[5].unpack()[2] == ""))
# Make sure the match rule is in place before A leaves.
b.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus",
            "org.freedesktop.DBus", "GetId", None, None,
            Gio.DBusCallFlags.NONE, -1, None)
a.close_sync(None)

context = GLib.MainContext.default()
while not any(gone):
    context.iteration(True)

start = time.monotonic()
try:
    call(b, device, DEVICE, "Claim", GLib.Variant("(s)", ("",)), 20000)
except GLib.Error as e:
    print("refused " + (Gio.DBusError.get_remote_error(e) or e.message))
    sys.exit(0)
print("claimed %.1f" % (time.monotonic() - start))
call(b, device, DEVICE, "Release", None)
