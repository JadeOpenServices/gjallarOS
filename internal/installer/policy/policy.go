// Package policy derives non-interactive installer choices from typed input.
package policy

import "github.com/JadeOpenServices/gjallarOS/internal/installer/config"

type Features struct {
	AIEnable               bool
	AutoReboot             bool
	DebugFunctions         bool
	ContainersEnable       bool
	ClamshellEnable        bool
	USBGuardEnable         bool
	USBTrustEnforce        bool
	USBTrustTPMHandle      string
	NemuEnable             bool
	RecoveryEnable         bool
	PrebootLockEnable      bool
	SecureBootEnable       bool
	ManagementEnable       bool
	TouchpadWorkspaceSwipe bool
	PrintingEnable         bool
	NetworkPrintingEnable  bool
}

func FromUser(user config.User) Features {
	return Features{
		AIEnable: user.HasApp("ai"), AutoReboot: user.AutoReboot,
		DebugFunctions: user.DebugFunctions, ContainersEnable: user.HasApp("containers"),
		ClamshellEnable: user.ClamshellEnable, USBGuardEnable: user.USBGuardEnable, USBTrustEnforce: user.USBTrustEnforce, USBTrustTPMHandle: user.USBTrustTPMHandle, PrintingEnable: user.PrintingEnable, NetworkPrintingEnable: user.NetworkPrintingEnable,
		NemuEnable:     user.HasApp("nemu"),
		RecoveryEnable: user.RecoveryEnable, PrebootLockEnable: user.PrebootLock.Enable,
		SecureBootEnable:       user.SecureBootEnable,
		ManagementEnable:       user.Management.Enable,
		TouchpadWorkspaceSwipe: user.TouchpadWorkspaceSwipe,
	}
}
