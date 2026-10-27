package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/nettrust"
)

var (
	vpnStatusPath   = "/run/gjallar/vpn-trust.json"
	vpnPolicyPath   = "/etc/gjallar/vpn-trust.json"
	vpnOverridePath = "/var/lib/gjallar/vpn-trust-override.json"
	// vpnReapply runs the trust decision again after an override change.
	vpnReapply = func() error {
		return exec.Command("systemctl", "restart", "gjallar-vpn-trust.service").Run()
	}
	vpnEUID = os.Geteuid
	vpnNow  = time.Now
	// vpnBootBlock installs the boot table.
	vpnBootBlock = func(ctx context.Context) error {
		return nettrust.NewSystem().ApplyBootBlock(ctx)
	}
	// vpnPauseTimer runs the trust decision again when a pause runs out.
	vpnPauseTimer = func(after time.Duration) error {
		exec.Command("systemctl", "stop", "gjallar-vpn-pause-end.timer").Run()
		return exec.Command("systemd-run", "--quiet", "--collect", "--unit=gjallar-vpn-pause-end",
			fmt.Sprintf("--on-active=%ds", int(after.Seconds())+1), "--timer-property=AccuracySec=1s",
			"systemctl", "restart", "--no-block", "gjallar-vpn-trust.service").Run()
	}
)

const vpnUsage = `usage: gjallarctl vpn status
       gjallarctl vpn trust-wifi [NAME] [--exit-node NODE|auto|default]
                                           trust NAME, or the current Wi-Fi;
                                           --exit-node still sends its internet
                                           through NODE
       gjallarctl vpn untrust-wifi [NAME]
       gjallarctl vpn exit-node NAME|auto|off|default
       gjallarctl vpn pause                let a network the VPN blocks reach the
                                           internet without the tunnel, for up to
                                           1 hour; asks an administrator's password
       gjallarctl vpn resume               end the pause now
       gjallarctl vpn notice               the desktop notice when the VPN blocks
                                           a network (runs in the session)
       gjallarctl vpn export               print the state.nix line for these changes
       gjallarctl vpn apply --policy FILE [--override FILE]
       gjallarctl vpn boot-block --policy FILE [--override FILE]
                                           at boot, block the internet outside the
                                           tunnel until the first network check`

func runVPN(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, vpnUsage)
		return 2
	}
	switch args[0] {
	case "apply":
		return runVPNApply(args[1:], stdout, stderr)
	case "boot-block":
		return runVPNBootBlock(args[1:], stdout, stderr)
	case "status":
		return runVPNStatus(stdout, stderr)
	case "trust-wifi", "untrust-wifi", "exit-node":
		return runVPNOverride(args[0], args[1:], stdout, stderr)
	case "export":
		return runVPNExport(stdout, stderr)
	case "pause":
		return runVPNPause(args[1:], stdout, stderr)
	case "resume":
		return runVPNResume(args[1:], stdout, stderr)
	case "notice":
		return runVPNNotice(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "ERROR: unknown vpn command %q\n", args[0])
		return 2
	}
}

func runVPNApply(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("vpn apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "network trust policy JSON")
	overridePath := flags.String("override", "", "runtime override JSON layered over the policy")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *policyPath == "" {
		fmt.Fprintln(stderr, "ERROR: --policy is required")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl vpn apply changes routing and must run as root")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	policy, err := nettrust.LoadPolicy(*policyPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if *overridePath != "" {
		// A broken override must not leave the network undecided.
		if override, err := nettrust.LoadOverride(*overridePath); err != nil {
			fmt.Fprintf(stderr, "WARN: ignoring override: %v\n", err)
		} else {
			policy = override.Apply(policy)
		}
	}

	sys := nettrust.NewSystem()
	network, err := sys.Network(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: observe network: %v\n", err)
		return 1
	}
	tailnet, err := sys.Tailnet(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: observe tailnet: %v\n", err)
		return 1
	}

	verdict, err := nettrust.Decide(ctx, policy, network, tailnet, sys)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	verdict.Network = nettrust.NetworkKey(network)
	applyVPNPause(&verdict, stdout, stderr)

	for _, warning := range verdict.Warnings {
		fmt.Fprintf(stderr, "WARN: %s\n", warning)
	}

	code := 0
	if err := sys.ApplyBypass(ctx, verdict.Bypass); err != nil {
		fmt.Fprintf(stderr, "ERROR: apply LAN bypass: %v\n", err)
		code = 1
	}
	if verdict.ManageExitNode {
		changed, err := sys.ApplyExitNode(ctx, tailnet, verdict.ExitNode)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "ERROR: set exit node: %v\n", err)
			verdict.ExitNodeError = err.Error()
			if verdict.ExitNode != "" {
				verdict.Blocked = nettrust.BlockNoExitNode
			}
			code = 1
		case changed && verdict.ExitNode == "":
			fmt.Fprintln(stdout, "VPN: exit node off")
		case changed:
			fmt.Fprintf(stdout, "VPN: exit node %s on\n", verdict.ExitNode)
		}
	}

	// Without a network the block stays as it is, like the exit node.
	if network != nil {
		if err := sys.ApplyBlock(ctx, network, verdict.Blocked != ""); err != nil {
			fmt.Fprintf(stderr, "ERROR: apply internet block: %v\n", err)
			code = 1
		}
	}
	if verdict.Blocked != "" {
		fmt.Fprintf(stderr, "WARN: %s\n", nettrust.BlockText(verdict.Blocked))
	}

	fmt.Fprintf(stdout, "network %s: %s; bypass [%s]\n",
		verdict.Trust, verdict.Reason, strings.Join(verdict.Bypass, " "))

	if err := nettrust.WriteStatus(vpnStatusPath, verdict); err != nil {
		fmt.Fprintf(stderr, "WARN: write %s: %v\n", vpnStatusPath, err)
	}
	return code
}

// runVPNBootBlock runs before networking starts. When the policy can ask
// for an exit node, nothing may leave outside the tunnel until the first
// verdict has looked at the network; that verdict replaces the table or
// removes it.
func runVPNBootBlock(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("vpn boot-block", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "network trust policy JSON")
	overridePath := flags.String("override", "", "runtime override JSON layered over the policy")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *policyPath == "" {
		fmt.Fprintln(stderr, "ERROR: --policy is required")
		return 2
	}
	if vpnEUID() != 0 {
		fmt.Fprintln(stderr, "ERROR: gjallarctl vpn boot-block changes the firewall and must run as root")
		return 1
	}
	// /run is empty at boot. A verdict here means the system is already
	// up, and its table is the right one.
	if _, err := os.Stat(vpnStatusPath); err == nil {
		fmt.Fprintln(stdout, "VPN: the network is already checked; no boot block")
		return 0
	}

	policy, err := nettrust.LoadPolicy(*policyPath)
	if err != nil {
		// The policy may want an exit node; the block stays until it can
		// be read.
		fmt.Fprintf(stderr, "WARN: %v; blocking anyway\n", err)
	} else {
		if *overridePath != "" {
			if override, err := nettrust.LoadOverride(*overridePath); err != nil {
				fmt.Fprintf(stderr, "WARN: ignoring override: %v\n", err)
			} else {
				policy = override.Apply(policy)
			}
		}
		if !policy.MayBlock() {
			fmt.Fprintln(stdout, "VPN: no exit node is required; no boot block")
			return 0
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := vpnBootBlock(ctx); err != nil {
		fmt.Fprintf(stderr, "ERROR: apply boot block: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "VPN: %s\n", nettrust.BootBlockText)
	return 0
}

// applyVPNPause lets a pause from the settings service carry a blocked
// network's traffic without the tunnel, and ends a pause that no longer
// holds: another network, past its end, or nothing blocked any more.
func applyVPNPause(v *nettrust.Verdict, stdout, stderr io.Writer) {
	pause, ok, err := nettrust.LoadPause(nettrust.PausePath)
	if err != nil {
		fmt.Fprintf(stderr, "WARN: ignoring VPN pause: %v\n", err)
	}
	if !ok {
		if err != nil {
			nettrust.RemovePause(nettrust.PausePath)
		}
		return
	}
	now := vpnNow()
	if v.Blocked != "" && pause.Holds(v.Network, now) {
		v.Paused = &nettrust.PauseState{User: pause.User, Until: pause.Until, Blocked: v.Blocked}
		fmt.Fprintf(stdout, "VPN: paused by %s on network %s until %s\n", pause.User, v.Network, pause.Until.Local().Format("15:04"))
		v.Blocked = ""
		v.ExitNode = ""
		if err := vpnPauseTimer(pause.Until.Sub(now)); err != nil {
			fmt.Fprintf(stderr, "WARN: schedule the end of the VPN pause: %v\n", err)
		}
		return
	}
	if err := nettrust.RemovePause(nettrust.PausePath); err != nil {
		fmt.Fprintf(stderr, "WARN: end VPN pause: %v\n", err)
		return
	}
	fmt.Fprintf(stdout, "VPN: pause by %s on network %s ended\n", pause.User, pause.Network)
}

func runVPNStatus(stdout, stderr io.Writer) int {
	_, err := os.Stat(nettrust.BootBlockPath)
	bootBlocked := err == nil
	data, err := os.ReadFile(vpnStatusPath)
	if err != nil {
		if bootBlocked && os.IsNotExist(err) {
			fmt.Fprintf(stdout, "network:   not checked yet\nblocked:   %s (systemctl status gjallar-vpn-trust)\n", nettrust.BootBlockText)
			return 0
		}
		fmt.Fprintf(stderr, "ERROR: no network trust decision yet: %v\n", err)
		return 1
	}
	var v nettrust.Verdict
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Fprintf(stderr, "ERROR: parse %s: %v\n", vpnStatusPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "network:   %s\nreason:    %s\n", v.Trust, v.Reason)
	if v.SSID != "" {
		fmt.Fprintf(stdout, "wifi:      %s\n", v.SSID)
	}
	if v.Router != "" {
		fmt.Fprintf(stdout, "router:    %s\n", v.Router)
	}
	exit := "unmanaged"
	if v.ManageExitNode {
		exit = "off"
		if v.ExitNode != "" {
			exit = v.ExitNode
		}
		if v.ExitNodeError != "" {
			exit += " (NOT ACTIVE: " + v.ExitNodeError + ")"
		}
	}
	local := strings.Join(v.Bypass, " ")
	if local == "" {
		local = "none"
	}
	fmt.Fprintf(stdout, "exit node: %s\nlocal:     %s\n", exit, local)
	if v.Blocked != "" {
		fmt.Fprintf(stdout, "blocked:   %s (gjallarctl vpn pause lets this network through for up to 1 hour)\n", nettrust.BlockText(v.Blocked))
	}
	if bootBlocked {
		// The last verdict had no network to look at.
		fmt.Fprintf(stdout, "blocked:   %s (systemctl status gjallar-vpn-trust)\n", nettrust.BootBlockText)
	}
	if p := v.Paused; p != nil {
		fmt.Fprintf(stdout, "paused:    by %s until %s; traffic on this network goes out without the tunnel\n",
			p.User, p.Until.Local().Format("15:04"))
	}
	if o, err := nettrust.LoadOverride(vpnOverridePath); err != nil {
		fmt.Fprintf(stdout, "override:  unreadable: %v\n", err)
	} else if !o.Empty() {
		fmt.Fprintf(stdout, "override:  %s (gjallarctl vpn export)\n", describeOverride(o))
	}
	for _, w := range v.Warnings {
		fmt.Fprintf(stdout, "warning:   %s\n", w)
	}
	return 0
}

// runVPNPause asks the settings service to pause the block. The service
// asks an administrator's password in this login session, and only on the
// local seat, so root has no session to ask in.
func runVPNPause(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, vpnUsage)
		return 2
	}
	if vpnEUID() == 0 {
		fmt.Fprintln(stderr, "ERROR: run gjallarctl vpn pause as your own user at this machine; it asks an administrator's password there")
		return 1
	}
	answer, err := vpnSettingsCall("PauseVPN")
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	var state nettrust.PauseState
	if err := json.Unmarshal([]byte(answer), &state); err != nil {
		fmt.Fprintf(stderr, "ERROR: read the service's answer: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "VPN paused on this network until %s. Apps here reach the internet without the tunnel.\n"+
		"It ends when the network changes or the device sleeps; gjallarctl vpn resume ends it now.\n",
		state.Until.Local().Format("15:04"))
	return 0
}

// runVPNResume ends a pause. Root removes the file itself: the sleep hook
// runs this, and the block must be back before the device sleeps.
func runVPNResume(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, vpnUsage)
		return 2
	}
	if vpnEUID() != 0 {
		if _, err := vpnSettingsCall("ResumeVPN"); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "VPN pause ended")
		return 0
	}
	if _, err := os.Lstat(nettrust.PausePath); os.IsNotExist(err) {
		fmt.Fprintln(stdout, "the VPN is not paused")
		return 0
	}
	if err := nettrust.RemovePause(nettrust.PausePath); err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	if err := vpnReapply(); err != nil {
		fmt.Fprintf(stderr, "ERROR: the pause is gone, but the VPN decision did not run again: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "VPN pause ended")
	return 0
}

func vpnSettingsCall(method string) (string, error) {
	bus, err := settingsConnect()
	if err != nil {
		return "", fmt.Errorf("system bus: %w", err)
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), settingsCallTimeout)
	defer cancel()
	return bus.Call(ctx, method)
}

func describeOverride(o nettrust.Override) string {
	var parts []string
	for _, ssid := range o.TrustWifis {
		parts = append(parts, fmt.Sprintf("trust %q", ssid))
	}
	for _, ssid := range o.UntrustWifis {
		parts = append(parts, fmt.Sprintf("untrust %q", ssid))
	}
	if o.ExitNode != nil {
		parts = append(parts, "exit node "+*o.ExitNode)
	}
	for _, ssid := range slices.Sorted(maps.Keys(o.WifiExitNodes)) {
		node := o.WifiExitNodes[ssid]
		if node == "" {
			node = "off"
		}
		parts = append(parts, fmt.Sprintf("exit node %s on %q", node, ssid))
	}
	return strings.Join(parts, ", ")
}

func runVPNOverride(cmd string, args []string, stdout, stderr io.Writer) int {
	var wifiExitNode *string
	if cmd == "trust-wifi" {
		var ok bool
		if args, wifiExitNode, ok = exitNodeFlag(args); !ok {
			fmt.Fprintln(stderr, vpnUsage)
			return 2
		}
	}
	if len(args) > 1 || (cmd == "exit-node" && len(args) != 1) {
		fmt.Fprintln(stderr, vpnUsage)
		return 2
	}
	if vpnEUID() != 0 {
		fmt.Fprintf(stderr, "ERROR: gjallarctl vpn %s changes network trust and must run as root\n", cmd)
		return 1
	}
	base, err := nettrust.LoadPolicy(vpnPolicyPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	override, err := nettrust.LoadOverride(vpnOverridePath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	switch cmd {
	case "exit-node":
		if err := override.SetExitNode(base, args[0]); err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 2
		}
	default:
		ssid, err := wifiArg(args)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
			return 1
		}
		if cmd == "trust-wifi" {
			override.Trust(base, ssid)
			if wifiExitNode != nil {
				if err := override.SetWifiExitNode(base, ssid, *wifiExitNode); err != nil {
					fmt.Fprintf(stderr, "ERROR: %v\n", err)
					return 2
				}
			}
		} else {
			override.Untrust(base, ssid)
		}
	}

	if err := nettrust.SaveOverride(vpnOverridePath, override); err != nil {
		fmt.Fprintf(stderr, "ERROR: save %s: %v\n", vpnOverridePath, err)
		return 1
	}
	if err := vpnReapply(); err != nil {
		fmt.Fprintf(stderr, "ERROR: reapply network trust: %v\n", err)
		return 1
	}
	return runVPNStatus(stdout, stderr)
}

// exitNodeFlag takes --exit-node NODE or --exit-node=NODE out of args, so
// it may come before or after the Wi-Fi name.
func exitNodeFlag(args []string) (rest []string, node *string, ok bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--exit-node" || arg == "-exit-node":
			if i+1 == len(args) || node != nil {
				return nil, nil, false
			}
			i++
			node = &args[i]
		case strings.HasPrefix(arg, "--exit-node=") || strings.HasPrefix(arg, "-exit-node="):
			if node != nil {
				return nil, nil, false
			}
			value := arg[strings.IndexByte(arg, '=')+1:]
			node = &value
		case strings.HasPrefix(arg, "-") && arg != "-":
			return nil, nil, false
		default:
			rest = append(rest, arg)
		}
	}
	return rest, node, true
}

// wifiArg returns the named Wi-Fi, or the current one when none is named.
func wifiArg(args []string) (string, error) {
	if len(args) == 1 {
		if ssid := args[0]; ssid != "" && len(ssid) <= 32 {
			return ssid, nil
		}
		return "", fmt.Errorf("%q is not a Wi-Fi name (1-32 bytes)", args[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	network, err := nettrust.NewSystem().Network(ctx)
	if err != nil {
		return "", fmt.Errorf("observe network: %w", err)
	}
	if network == nil || network.SSID == "" {
		return "", fmt.Errorf("not on Wi-Fi; name the network")
	}
	if !network.Secured() {
		return "", fmt.Errorf("Wi-Fi %q is open; a trusted name only counts on a network that needs a key", network.SSID)
	}
	return network.SSID, nil
}

func runVPNExport(stdout, stderr io.Writer) int {
	base, err := nettrust.LoadPolicy(vpnPolicyPath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	override, err := nettrust.LoadOverride(vpnOverridePath)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, nettrust.NixState(override.Apply(base)))
	fmt.Fprintf(stderr, "Replace the tailscale line in generated/state.nix with this, rebuild, then remove %s.\n", vpnOverridePath)
	return 0
}
