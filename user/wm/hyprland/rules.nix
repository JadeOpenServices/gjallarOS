{ ... }:

{
  wayland.windowManager.hyprland.settings = {
    windowrule = [
      # USB guard dialogs: float centered, sized relative to the monitor.
      "match:title ^(Permanent USB trust|Authorize USB device|USB decision failed)$, float on, center on, size (monitor_w*0.4) (monitor_h*0.5), max_size (monitor_w*0.8) (monitor_h*0.85)"
      # The review sizes itself so every action row shows; a size here would
      # override that and hide the last row behind a scrollbar.
      "match:title ^(USB device review)$, float on, center on, max_size (monitor_w*0.8) (monitor_h*0.85)"
      # Details of the security notice (gjallarctl security notice): big enough
      # that ODDC's whole text shows without scrolling up.
      "match:title ^(Security notes for this device)$, float on, center on, size (monitor_w*0.6) (monitor_h*0.8)"
      # The time details (bar clock click, gjallar-time-details). The window
      # sizes itself in cells so no line wraps; a size here would override that.
      "match:title ^(GjallarOS time)$, float on, center on, max_size (monitor_w*0.9) (monitor_h*0.8)"
    ];

    workspace = [
      "special,gapsin:24,gapsout:64"
    ];
  };
}
