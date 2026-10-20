package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/settings"
)

// gjallarctl settings pair (ROADMAP "Pairing and own keys", step 3.4c). A
// pairing file goes each way between two devices of the same person; each
// device shows a code made from both keys, and the user adds the other
// device only after the codes match.

const settingsPairUsage = `usage: gjallarctl settings pair export FILE
       gjallarctl settings pair add FILE
       gjallarctl settings pair list [--ids]
       gjallarctl settings pair remove ID`

func settingsPair(ctx context.Context, bus settingsBus, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, settingsPairUsage)
		return 2
	}
	var err error
	switch args[0] {
	case "export":
		err = settingsPairExport(ctx, bus, args[1:], stdout)
	case "add":
		err = settingsPairAdd(ctx, bus, args[1:], stdout)
	case "list":
		err = settingsPairList(ctx, bus, args[1:], stdout)
	case "remove":
		err = settingsPairRemove(ctx, bus, args[1:], stdout)
	default:
		fmt.Fprintf(stderr, "ERROR: unknown pair command %q\n%s\n", args[0], settingsPairUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

func settingsPairExport(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: gjallarctl settings pair export FILE")
	}
	fmt.Fprintln(stdout, "The password dialog asks for your own password.")
	data, err := bus.Bytes(ctx, "PairExport")
	if err != nil {
		return err
	}
	if err := writeNewFile(args[0], data); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved to %s. Take it to your other device, by USB stick, Nextcloud or mail, and run there:\n", args[0])
	fmt.Fprintf(stdout, "  gjallarctl settings pair add %s\n", args[0])
	fmt.Fprintln(stdout, "Then make a pairing file there and add it here the same way.")
	return nil
}

func settingsPairAdd(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: gjallarctl settings pair add FILE")
	}
	data, err := readLimited(args[0], "a pairing file", settings.MaxPairBytes)
	if err != nil {
		return err
	}
	answer, err := bus.Call(ctx, "PairCheck", data)
	if err != nil {
		return err
	}
	var offer settings.PairOffer
	if err := json.Unmarshal([]byte(answer), &offer); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Pairing file of %q on the device that says it is %q.\n", offer.User, offer.Hostname)
	fmt.Fprintln(stdout, "Code:")
	groups := strings.Fields(offer.Code)
	for i := 0; i < len(groups); i += 4 {
		fmt.Fprintf(stdout, "  %s\n", strings.Join(groups[i:min(i+4, len(groups))], " "))
	}
	fmt.Fprintln(stdout, "The other device shows its code when it adds your pairing file. Compare the two")
	fmt.Fprintln(stdout, "screens side by side, or read the code out on a call; not over the way the files came.")
	ui := prompt.UI{Reader: bufio.NewReader(settingsStdin), Out: stdout}
	same, err := ui.Confirm(ctx, "Does the other device show exactly this code?", false)
	if err != nil {
		return err
	}
	if !same {
		fmt.Fprintln(stdout, "Not paired. If the codes differ, a file may have been changed on the way: make new pairing files on both devices.")
		return nil
	}
	fmt.Fprintln(stdout, "The password dialog asks for your own password.")
	answer, err = bus.Call(ctx, "PairAdd", data, offer.Code)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(answer), &offer); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Paired with %q on %q (id %s). Settings files of that account import here now,\n", offer.User, offer.Hostname, offer.ID)
	fmt.Fprintln(stdout, "without the settings that belong to its device. Remove it with gjallarctl settings pair remove "+offer.ID)
	return nil
}

func settingsPairList(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	ids := len(args) == 1 && args[0] == "--ids"
	if len(args) > 0 && !ids {
		return fmt.Errorf("usage: gjallarctl settings pair list [--ids]")
	}
	answer, err := bus.Call(ctx, "PairList")
	if err != nil {
		return err
	}
	var list []settings.PairOffer
	if err := json.Unmarshal([]byte(answer), &list); err != nil {
		return err
	}
	for _, offer := range list {
		if ids {
			fmt.Fprintln(stdout, offer.ID)
		} else {
			fmt.Fprintf(stdout, "%s  %q on the device that says it is %q, paired %s\n",
				offer.ID, offer.User, offer.Hostname, offer.Added.In(settingsNow().Location()).Format("2006-01-02 15:04"))
		}
	}
	if len(list) == 0 && !ids {
		fmt.Fprintln(stdout, "No paired devices. Start with gjallarctl settings pair export FILE")
	}
	return nil
}

func settingsPairRemove(ctx context.Context, bus settingsBus, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: gjallarctl settings pair remove ID")
	}
	if _, err := bus.Call(ctx, "PairRemove", args[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed pairing %s. Settings files from that device no longer import here.\n", args[0])
	return nil
}
