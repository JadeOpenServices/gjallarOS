package oddccli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/oddcvalidation"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/release"
	"github.com/JadeOpenServices/gjallarOS/internal/installercheck"
)

// validation runs the device gates and recorder keeps a passed result;
// tests replace both.
type (
	validation func(
		context.Context,
		string,
		oddcvalidation.CommandRunner,
	) (oddcvalidation.Report, oddcvalidation.DeviceContext, error)
	recorder func(string, oddcvalidation.Report, oddcvalidation.DeviceContext) error
)

func runValidateDevice(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gjallarctl oddc validate-device", flag.ContinueOnError)
	flags.SetOutput(stderr)

	repo := flags.String("repo", ".", "gjallarOS repository root")

	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "ERROR: oddc validate-device accepts no positional arguments")
		return 2
	}

	root, err := installercheck.ResolveRepository(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 2
	}

	return validateDevice(root, stdout, stderr, oddcvalidation.Run, record)
}

func validateDevice(
	root string,
	stdout io.Writer,
	stderr io.Writer,
	run validation,
	record recorder,
) int {
	report, device, err := run(context.Background(), root, nil)
	if err == nil {
		err = record(root, report, device)
		if err == nil {
			fmt.Fprintln(stdout, "PASS: validation recorded locally")
		}
	}

	for _, result := range report.Results {
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		}
		if result.Details != "" {
			fmt.Fprintf(stdout, "%s: %s - %s\n", status, result.Gate, result.Details)
		} else {
			fmt.Fprintf(stdout, "%s: %s\n", status, result.Gate)
		}
	}

	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "PASS: ODDC real-device validation complete")
	return 0
}

// record keeps the validation on this machine. Evidence reaches ODDC
// through `oddc contribute`, not through this repository.
func record(
	root string,
	report oddcvalidation.Report,
	device oddcvalidation.DeviceContext,
) error {
	pinnedRelease, err := release.Expected(root)
	if err != nil {
		return err
	}

	validation, err := oddcvalidation.ValidationMetadata(
		report,
		device,
		pinnedRelease,
		time.Now(),
	)
	if err != nil {
		return err
	}

	return oddcvalidation.WriteLocalValidation(context.Background(), validation)
}
