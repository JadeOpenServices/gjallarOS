package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/diskplan"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryprovision"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/recoveryresize"
)

const targetRoot = "/mnt"

func main() {
	if err := run(
		context.Background(),
		os.Args[1:],
		os.Stdin,
		os.Stdout,
	); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	in io.Reader,
	out io.Writer,
) error {
	flags := flag.NewFlagSet(
		"gjallar-recovery-maintenance",
		flag.ContinueOnError,
	)
	flags.SetOutput(out)

	var rootPartition string
	var mappingPath string

	flags.StringVar(
		&rootPartition,
		"root-partition",
		"",
		"stable LUKS root partition device",
	)
	flags.StringVar(
		&mappingPath,
		"mapping",
		"",
		"root dm-crypt mapping path",
	)

	if err := flags.Parse(args); err != nil {
		return err
	}

	rootPartition = strings.TrimSpace(rootPartition)
	mappingPath = strings.TrimSpace(mappingPath)

	if rootPartition == "" ||
		!filepath.IsAbs(rootPartition) ||
		!strings.HasPrefix(filepath.Clean(rootPartition), "/dev/") {
		return fmt.Errorf(
			"root partition must be an absolute device under /dev",
		)
	}

	if mappingPath == "" ||
		!filepath.IsAbs(mappingPath) ||
		!strings.HasPrefix(
			filepath.Clean(mappingPath),
			"/dev/mapper/",
		) {
		return fmt.Errorf(
			"mapping must be an absolute /dev/mapper path",
		)
	}

	mappingName := filepath.Base(mappingPath)
	if mappingName == "." ||
		mappingName == "/" ||
		mappingName == "" {
		return fmt.Errorf("invalid dm-crypt mapping path")
	}

	secret, err := io.ReadAll(io.LimitReader(in, 64*1024))
	if err != nil {
		return fmt.Errorf("read LUKS credential: %w", err)
	}

	secret = bytes.TrimRight(secret, "\r\n")
	if len(secret) == 0 {
		return fmt.Errorf("LUKS credential is empty")
	}

	defer func() {
		for index := range secret {
			secret[index] = 0
		}
	}()

	runner := recoveryresize.SystemRunner()

	mappingOpenedHere := false
	rootMounted := false

	defer func() {
		if rootMounted {
			_ = runner.Run(
				context.Background(),
				"umount",
				targetRoot,
			)
		}

		if mappingOpenedHere {
			_ = runner.Run(
				context.Background(),
				"cryptsetup",
				"close",
				mappingName,
			)
		}
	}()

	if _, err := os.Stat(mappingPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf(
				"inspect root mapping %s: %w",
				mappingPath,
				err,
			)
		}

		if err := runner.RunInput(
			ctx,
			secret,
			"cryptsetup",
			"open",
			"--type",
			"luks",
			"--key-file=-",
			rootPartition,
			mappingName,
		); err != nil {
			return fmt.Errorf(
				"unlock encrypted root: %w",
				err,
			)
		}

		mappingOpenedHere = true
	}

	if err := runner.Run(
		ctx,
		"mkdir",
		"-p",
		targetRoot,
	); err != nil {
		return fmt.Errorf(
			"create maintenance root mountpoint: %w",
			err,
		)
	}

	if err := runner.Run(
		ctx,
		"mount",
		"-o",
		"rw",
		mappingPath,
		targetRoot,
	); err != nil {
		return fmt.Errorf(
			"mount encrypted root at /mnt: %w",
			err,
		)
	}

	rootMounted = true

	resolvedRoot, err := filepath.EvalSymlinks(rootPartition)
	if err != nil {
		return fmt.Errorf(
			"resolve root partition %s: %w",
			rootPartition,
			err,
		)
	}

	parentRaw, err := runner.Output(
		ctx,
		"lsblk",
		"-dnro",
		"PKNAME",
		resolvedRoot,
	)
	if err != nil {
		return fmt.Errorf(
			"resolve root parent disk: %w",
			err,
		)
	}

	parent := strings.TrimSpace(string(parentRaw))
	if parent == "" {
		return fmt.Errorf(
			"root partition %s has no parent disk",
			resolvedRoot,
		)
	}
	if !strings.HasPrefix(parent, "/dev/") {
		parent = filepath.Join("/dev", parent)
	}

	topology, err := recoveryresize.DiscoverTopology(
		ctx,
		runner,
		recoveryresize.DiscoveryInput{
			RootMountpoint:            targetRoot,
			ExpectedDiskPath:          parent,
			ExpectedRootPartitionPath: resolvedRoot,
			ExpectedMappingPath:       mappingPath,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"discover maintenance topology: %w",
			err,
		)
	}

	plan, err := recoveryprovision.BuildExistingPlan(
		ctx,
		runner,
		topology,
	)
	if err != nil && !errors.As(err, new(*recoveryprovision.RecoveryPresentError)) {
		return fmt.Errorf(
			"build existing-layout recovery plan: %w",
			err,
		)
	}

	var present *recoveryprovision.RecoveryPresentError
	if errors.As(err, &present) {
		fmt.Fprintf(
			out,
			"PASS: recovery partition already present at %s; no storage changes were made\n",
			present.Partition,
		)
	} else if err := provision(ctx, runner, out, topology, plan, secret); err != nil {
		return err
	}

	stateDir := filepath.Join(
		targetRoot,
		"var/lib/gjallarOS",
	)

	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf(
			"create maintenance completion directory: %w",
			err,
		)
	}

	if err := os.WriteFile(
		filepath.Join(
			stateDir,
			"recovery-maintenance-complete",
		),
		[]byte("recovery partition ready\n"),
		0600,
	); err != nil {
		return fmt.Errorf(
			"record maintenance completion: %w",
			err,
		)
	}

	fmt.Fprintln(
		out,
		"PASS: installed encrypted Btrfs root remained mounted only at /mnt",
	)
	fmt.Fprintln(
		out,
		"PASS: maintenance completion recorded",
	)

	return nil
}

func provision(
	ctx context.Context,
	runner recoveryresize.ExecutorRunner,
	out io.Writer,
	topology recoveryresize.Topology,
	plan diskplan.Plan,
	secret []byte,
) error {
	prepared, err := recoveryprovision.Prepare(
		ctx,
		runner,
		recoveryprovision.Input{
			Plan:                      plan,
			UI:                        prompt.New(nil, out),
			Out:                       out,
			RootMountpoint:            targetRoot,
			ExpectedRootPartitionPath: topology.RootPartitionPath,
			ExpectedMappingPath:       topology.MappingPath,
			Unattended:                true,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"prepare recovery storage: %w",
			err,
		)
	}

	switch prepared.Status {
	case recoveryprovision.StatusReady:
		fmt.Fprintf(
			out,
			"PASS: exact 12 GiB recovery partition ready at %s\n",
			prepared.RecoveryPartition,
		)

	case recoveryprovision.StatusResizeRequired:
		result, err := recoveryprovision.ExecuteMaintenance(
			ctx,
			runner,
			recoveryprovision.MaintenanceInput{
				Plan:       plan,
				Passphrase: secret,
				Out:        out,
			},
		)
		if err != nil {
			return err
		}

		fmt.Fprintf(
			out,
			"PASS: recovery maintenance completed at %s\n",
			result.RecoveryPartition,
		)

	default:
		return fmt.Errorf(
			"unexpected recovery preparation status %q",
			prepared.Status,
		)
	}

	return nil
}
