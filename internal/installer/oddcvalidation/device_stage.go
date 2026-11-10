package oddcvalidation

import (
	"context"
	"os"

	"github.com/JadeOpenServices/gjallarOS/internal/hardware/graphics"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/discovery"
)

type ExecutableResolver func() (string, error)

func RunDeviceStage(
	ctx context.Context,
	repo string,
	runner CommandRunner,
) (Report, DeviceContext, error) {
	return runDeviceStage(
		ctx,
		repo,
		runner,
		LoadDeviceContext,
		os.Executable,
	)
}

func runDeviceStage(
	ctx context.Context,
	repo string,
	runner CommandRunner,
	loadContext func(string) (DeviceContext, error),
	resolveExecutable ExecutableResolver,
) (Report, DeviceContext, error) {
	if runner == nil {
		runner = defaultCommandRunner
	}

	device, err := loadContext(repo)
	if err != nil {
		return Report{}, DeviceContext{}, err
	}

	executable, err := resolveExecutable()
	if err != nil {
		return Report{}, DeviceContext{}, err
	}

	results := []Result{
		runRealMachineRebuild(
			ctx,
			runner,
			repo,
			executable,
		),
	}

	results = append(
		results,
		runtimeGraphicsResult(ctx, runner, graphics.Detect, repo),
		runtimeInputResult(ctx, runner, discovery.DetectHardware, repo),
		secureBootPolicyResult(device.Resolved),
	)

	return Report{Results: results}, device, nil
}
