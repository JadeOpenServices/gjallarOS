// Command gjallar-settingsd is the root settings service (ROADMAP section
// 14, step 3.3). It owns org.gjallaros.Settings1 on the system bus, checks
// every change to user.config.json against its tier, asks polkit for the
// password, keeps the wait, writes the file and starts the rebuild.
// Pending changes live only in memory, so the service stays running once
// started.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/systemcheckout"
)

const introspection = `<node>
  <interface name="` + interfaceName + `">
    <method name="List"><arg name="keys" type="s" direction="out"/></method>
    <method name="Get">
      <arg name="key" type="s" direction="in"/>
      <arg name="value" type="s" direction="out"/>
    </method>
    <method name="Request">
      <arg name="values" type="s" direction="in"/>
      <arg name="change" type="s" direction="out"/>
    </method>
    <method name="RequestLocal">
      <arg name="files" type="a{say}" direction="in"/>
      <arg name="change" type="s" direction="out"/>
    </method>
    <method name="Apply">
      <arg name="id" type="s" direction="in"/>
      <arg name="colors" type="s" direction="in"/>
    </method>
    <method name="Pending"><arg name="changes" type="s" direction="out"/></method>
    <method name="Cancel"><arg name="id" type="s" direction="in"/></method>
    <method name="Export"><arg name="file" type="ay" direction="out"/></method>
    <method name="Import">
      <arg name="file" type="ay" direction="in"/>
      <arg name="change" type="s" direction="out"/>
    </method>
    <method name="PairExport"><arg name="file" type="ay" direction="out"/></method>
    <method name="PairCheck">
      <arg name="file" type="ay" direction="in"/>
      <arg name="offer" type="s" direction="out"/>
    </method>
    <method name="PairAdd">
      <arg name="file" type="ay" direction="in"/>
      <arg name="code" type="s" direction="in"/>
      <arg name="pairing" type="s" direction="out"/>
    </method>
    <method name="PairList"><arg name="pairings" type="s" direction="out"/></method>
    <method name="PairRemove"><arg name="id" type="s" direction="in"/></method>
  </interface>` + introspect.IntrospectDataString + `</node>`

func main() {
	log.SetFlags(0)
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: gjallar-settingsd (started by D-Bus)")
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		log.Fatal("gjallar-settingsd runs as root")
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Fatalf("connect to the system bus: %v", err)
	}
	defer conn.Close()

	svc := newService(busSystem{conn}, systemcheckout.Path, systemcheckout.CheckOwnership)
	svc.keys = loadKeyStore()
	if svc.device, err = deviceID("/etc/machine-id"); err != nil {
		log.Printf("settings files: %v; export and import are off", err)
		svc.keys = nil
	} else if _, err := svc.keys.trusted(); err != nil {
		log.Printf("settings files: %v", err)
	}
	if err := conn.Export(object{svc}, objectPath, interfaceName); err != nil {
		log.Fatal(err)
	}
	if err := conn.Export(introspect.Introspectable(introspection), objectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		log.Fatal(err)
	}

	// Changes die with the session that asked for them.
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.login1"),
		dbus.WithMatchObjectPath("/org/freedesktop/login1"),
		dbus.WithMatchInterface("org.freedesktop.login1.Manager"),
		dbus.WithMatchMember("SessionRemoved"),
	); err != nil {
		log.Fatalf("watch login sessions: %v", err)
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)

	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatalf("request %s: %v", busName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatalf("%s is already owned", busName)
	}
	log.Printf("serving %s", busName)

	for signal := range signals {
		if signal.Name != "org.freedesktop.login1.Manager.SessionRemoved" || len(signal.Body) == 0 {
			continue
		}
		if session, ok := signal.Body[0].(string); ok {
			svc.DropSession(session)
		}
	}
	log.Fatal("the system bus connection closed")
}
