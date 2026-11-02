package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vulkaninfo 1.4.341 --summary on a Framework 13 with an AMD 780M,
// captured 2026-11-02; the instance part and the UUID lines are cut.
const vkSample = "WARNING: [Loader Message] Code 0 : terminator_CreateInstance: Received return code -9 from call to vkCreateInstance in ICD libvulkan_dzn.so. Skipping this driver.\n" +
	"==========\nVULKANINFO\n==========\n\nVulkan Instance Version: 1.4.341\n\n" +
	"Devices:\n========\n" +
	"GPU0:\n" +
	"\tapiVersion         = 1.4.354\n" +
	"\tdriverVersion      = 26.1.8\n" +
	"\tvendorID           = 0x1002\n" +
	"\tdeviceID           = 0x15bf\n" +
	"\tdeviceType         = PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU\n" +
	"\tdeviceName         = AMD Radeon 780M Graphics (RADV PHOENIX)\n" +
	"\tdriverID           = DRIVER_ID_MESA_RADV\n" +
	"\tdriverName         = radv\n" +
	"\tdriverInfo         = Mesa 26.1.8\n" +
	"\tconformanceVersion = 1.4.0.0\n" +
	"GPU1:\n" +
	"\tapiVersion         = 1.4.354\n" +
	"\tdriverVersion      = 26.1.8\n" +
	"\tvendorID           = 0x10005\n" +
	"\tdeviceID           = 0x0000\n" +
	"\tdeviceType         = PHYSICAL_DEVICE_TYPE_CPU\n" +
	"\tdeviceName         = llvmpipe (LLVM 21.1.8, 256 bits)\n" +
	"\tdriverID           = DRIVER_ID_MESA_LLVMPIPE\n" +
	"\tdriverName         = llvmpipe\n" +
	"\tdriverInfo         = Mesa 26.1.8 (LLVM 21.1.8)\n" +
	"\tconformanceVersion = 1.3.1.1\n"

func TestParseVulkan(t *testing.T) {
	got := parseVulkan([]byte(vkSample))
	want := []vkDevice{
		{name: "AMD Radeon 780M Graphics (RADV PHOENIX)", api: "1.4.354", kind: "INTEGRATED_GPU", vendor: "0x1002", driver: "radv"},
		{name: "llvmpipe (LLVM 21.1.8, 256 bits)", api: "1.4.354", kind: "CPU", vendor: "0x10005", driver: "llvmpipe"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// SYNTHETIC: output that does not look as vulkaninfo writes it.
func TestParseVulkanRejects(t *testing.T) {
	in := "GPU0:\n\tapiVersion = 1.3.0\n\tdeviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n\tvendorID = 0x10de\n\tdeviceName = before Devices\n" +
		"Devices:\n" +
		"GPU0:\n\tapiVersion = one\n\tdeviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n\tvendorID = 0x10de\n\tdeviceName = bad version\n" +
		"GPU1:\n\tapiVersion = 1.3.0\n\tdeviceType = PHYSICAL_DEVICE_TYPE_ROCKET\n\tvendorID = 0x10de\n\tdeviceName = bad type\n" +
		"GPU2:\n\tapiVersion = 1.3.0\n\tdeviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n\tvendorID = 0x10de\n\tdeviceName = \x1b[31mred" + strings.Repeat("x", 100) + "\n\tdriverName = bad name!\n"
	got := parseVulkan([]byte(in))
	if len(got) != 1 {
		t.Fatalf("got %+v, want only GPU2", got)
	}
	d := got[0]
	if strings.ContainsRune(d.name, 0x1b) || len([]rune(d.name)) > 80 || !strings.HasPrefix(d.name, "[31mred") {
		t.Errorf("name not cleaned: %q", d.name)
	}
	if d.driver != "" {
		t.Errorf("driver %q kept", d.driver)
	}
}

func TestAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"1.4.354": true, "1.3.0": true, "1.2.199": false, "2.0.0": true, "0.9.1": false, "x.3.0": false} {
		if got := atLeast(v, vulkanNeed); got != want {
			t.Errorf("atLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

// SYNTHETIC sysfs tree in the kernel's layout.
func TestGPUs(t *testing.T) {
	root := t.TempDir()
	old := sysRoot
	sysRoot = root
	t.Cleanup(func() { sysRoot = old })
	drm := filepath.Join(root, "sys", "class", "drm")
	mk := func(card, vendor, bootVGA, driver string) {
		dev := filepath.Join(drm, card, "device")
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dev, "vendor"), []byte(vendor+"\n"), 0o644)
		if bootVGA != "" {
			os.WriteFile(filepath.Join(dev, "boot_vga"), []byte(bootVGA+"\n"), 0o644)
		}
		if driver != "" {
			os.Symlink("../../../bus/pci/drivers/"+driver, filepath.Join(dev, "driver"))
		}
	}
	mk("card0", "0x8086", "1", "i915")
	mk("card1", "0x10de", "0", "")
	mk("card2", "nonsense", "", "x")
	mk("card1-eDP-1", "0x8086", "", "")
	os.MkdirAll(filepath.Join(drm, "renderD128"), 0o755)
	got, err := gpus()
	if err != nil {
		t.Fatal(err)
	}
	want := []gpu{{card: "card0", vendor: "0x8086", driver: "i915", bootVGA: true}, {card: "card1", vendor: "0x10de"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("gpus() = %+v, want %+v", got, want)
	}
}

func TestGraphicsLines(t *testing.T) {
	amd := gpu{card: "card1", vendor: "0x1002", driver: "amdgpu", bootVGA: true}
	nv := gpu{card: "card0", vendor: "0x10de", driver: "nvidia"}
	devs := parseVulkan([]byte(vkSample))
	soft := devs[1:]
	cases := []struct {
		name  string
		cards []gpu
		devs  []vkDevice
		verr  error
		nt    ntsync
		has   []string
		not   []string
	}{
		{"fw13", []gpu{amd}, devs, nil, ntsyncNotLoaded,
			[]string{"GPU card1: AMD, driver amdgpu, shows the start screen", "Vulkan 1.3: AMD Radeon 780M Graphics (RADV PHOENIX) has it", "module that is not loaded"},
			[]string{"shows no Vulkan", "GPUs;"}},
		{"nvidia not in sandbox", []gpu{amd, nv}, devs, nil, ntsyncOn,
			[]string{"This device has 2 GPUs", "GPU card0 (NVIDIA, driver nvidia) shows no Vulkan", "does not pass it in yet"},
			nil},
		{"software only", []gpu{{card: "card0", vendor: "0x1af4", driver: "virtio-pci"}}, soft, nil, ntsyncNone,
			[]string{"Only software Vulkan", "GPU card0 (virtio, driver virtio-pci) shows no Vulkan", "not in this kernel"},
			[]string{"has it."}},
		{"no vulkan", []gpu{amd}, nil, errors.New("vulkaninfo ended with code 1"), ntsyncNone,
			[]string{"none seen (vulkaninfo ended with code 1)", "No GPU with Vulkan 1.3"},
			nil},
		{"old driver", []gpu{amd}, []vkDevice{{name: "old", api: "1.2.199", kind: "INTEGRATED_GPU", vendor: "0x1002"}}, nil, ntsyncNone,
			[]string{"No GPU with Vulkan 1.3"},
			[]string{"shows no Vulkan"}},
	}
	for _, c := range cases {
		text := strings.Join(graphicsLines(c.cards, c.devs, c.verr, c.nt), "\n")
		for _, s := range c.has {
			if !strings.Contains(text, s) {
				t.Errorf("%s: missing %q in\n%s", c.name, s, text)
			}
		}
		for _, s := range c.not {
			if strings.Contains(text, s) {
				t.Errorf("%s: has %q in\n%s", c.name, s, text)
			}
		}
	}
}
