{
  config,
  inputs,
  lib,
  pkgs,
  settings,
  ...
}:
let
  graphics = import (inputs.oddc + "/nixos/lib/graphics.nix") { inherit lib; } config.oddc.resolved;
  vendor = graphics.vendor;
  # No default: ODDC consumers fail closed when an NVIDIA model has no branch.
  driverBranch = lib.attrByPath [ "policy" "graphics" "discrete" "driverBranch" ] null config.oddc.resolved;
  legacy580 = vendor == "nvidia" && driverBranch == "legacy_580";
  hybrid = graphics.type == "hybrid";

  nvidiaPackage =
    if legacy580 then
      config.boot.kernelPackages.nvidiaPackages.legacy_580
    else
      config.boot.kernelPackages.nvidiaPackages.stable;
in
{
  assertions = lib.optionals (vendor == "nvidia") [
    {
      assertion = builtins.elem driverBranch [ "stable" "legacy_580" ];
      message = "ODDC model ${settings.oddcModel or "(none)"} has NVIDIA graphics, but its policy.graphics.discrete.driverBranch is ${if driverBranch == null then "missing" else "\"${driverBranch}\""}; it must be stable or legacy_580.";
    }
    # Bus IDs come from the hardware scan, the hybrid type from ODDC; say so
    # before nixpkgs' bare PRIME assertion does.
    {
      assertion = !hybrid || (settings.graphicsBusId != "" && settings.graphicsIntegratedBusId != "");
      message = ''
        ODDC model ${settings.oddcModel or "(none)"} has integrated + NVIDIA graphics, but the hardware scan did not find both GPUs (NVIDIA "${settings.graphicsBusId}", integrated "${settings.graphicsIntegratedBusId}").
        Turn the NVIDIA GPU on in firmware setup (hybrid graphics mode), then run the installer or `rebuild --hardware-update` again.'';
    }
  ];

  services.xserver.enable = true;
  hardware.graphics = {
    enable = true;
    enable32Bit = true;
  };

  boot.initrd.kernelModules = lib.concatLists [
    (lib.optional (vendor == "amd") "amdgpu")
    (lib.optional (vendor == "intel") "i915")
  ];
  services.xserver.videoDrivers = if vendor == "nvidia" then [ "nvidia" ] else [ "modesetting" ];

  environment.systemPackages =
    with pkgs;
    lib.optionals (vendor == "intel") [
      intel-media-driver
      intel-vaapi-driver
    ];

  hardware.nvidia = lib.mkIf (vendor == "nvidia") {
    package = nvidiaPackage;
    open = !legacy580;
    modesetting.enable = true;
    powerManagement.enable = true;
    prime = lib.mkIf hybrid {
      offload.enable = true;
      intelBusId = settings.graphicsIntegratedBusId;
      nvidiaBusId = settings.graphicsBusId;
    };
  };

  hardware.amdgpu.opencl.enable = lib.mkIf (vendor == "amd" && graphics.compute) true;
  hardware.graphics.extraPackages = lib.mkIf (vendor == "amd" && graphics.compute) (
    with pkgs;
    [
      rocmPackages.clr
      rocmPackages.clr.icd
    ]
  );
}
