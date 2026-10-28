{ ... }:

{
  wayland.windowManager.hyprland.settings = {
    window_rule = [
      # USB guard dialogs: float centered, sized relative to the monitor.
      {
        match.title = "^(Permanent USB trust|Authorize USB device|USB decision failed)$";
        float = true;
        center = true;
        size = [
          "monitor_w*0.4"
          "monitor_h*0.5"
        ];
        max_size = [
          "monitor_w*0.8"
          "monitor_h*0.85"
        ];
      }
      # The review sizes itself so every action row shows; a size here would
      # override that and hide the last row behind a scrollbar.
      {
        match.title = "^(USB device review)$";
        float = true;
        center = true;
        max_size = [
          "monitor_w*0.8"
          "monitor_h*0.85"
        ];
      }
      # Details of the security notice (gjallarctl security notice): big enough
      # that ODDC's whole text shows without scrolling up.
      {
        match.title = "^(Security notes for this device)$";
        float = true;
        center = true;
        size = [
          "monitor_w*0.6"
          "monitor_h*0.8"
        ];
      }
      # The time details (bar clock click, gjallar-time-details). The window
      # sizes itself in cells so no line wraps; a size here would override that.
      {
        match.title = "^(GjallarOS time)$";
        float = true;
        center = true;
        max_size = [
          "monitor_w*0.9"
          "monitor_h*0.8"
        ];
      }
      # Monique's monitor name overlay. Monique adds this rule at runtime as
      # well, but only once per run; a reload drops that copy.
      {
        match.title = "^(monique-osd)$";
        no_anim = true;
        no_focus = true;
        no_shadow = true;
        border_size = 0;
      }
    ];

    workspace_rule = [
      {
        workspace = "special";
        gaps_in = 24;
        gaps_out = 64;
      }
    ];
  };
}
