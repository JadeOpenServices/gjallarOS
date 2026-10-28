{ lib, ... }:

let
  hl = import ./lua.nix { inherit lib; };
in
{
  wayland.windowManager.hyprland.settings = {
    # Workaround W13 (Plane 262, wiki 752 Q29): Firefox extension pop-ups
    # such as Passbolt's. They map titled "Mozilla Firefox" and get their
    # "Extension: (...)" title a moment later, too late for a window rule.
    # The handler floats a Firefox window whose first new title is such a
    # title, 400x600 under the pointer: the pop-up closes when it loses
    # focus, and focus follows the mouse. Other Firefox windows keep their
    # first title and stay tiled. A page can at most get its own window
    # floated this way.
    extensionPopupTitles._var = hl.inline "{}";
    on = [
      {
        _args = [
          "window.open"
          (hl.inline ''
            function(w)
              if w.class == "firefox" then
                extensionPopupTitles[w.address] = w.initial_title
              end
            end'')
        ];
      }
      {
        _args = [
          "window.close"
          (hl.inline "function(w) extensionPopupTitles[w.address] = nil end")
        ];
      }
      {
        _args = [
          "window.title"
          (hl.inline ''
            function(w)
              local initial = extensionPopupTitles[w.address]
              if initial == nil or w.title == initial then
                return
              end
              extensionPopupTitles[w.address] = nil
              if w.floating or string.sub(w.title, 1, 12) ~= "Extension: (" then
                return
              end
              local m = w.monitor
              local mw, mh = m.size.width / m.scale, m.size.height / m.scale
              if m.transform % 2 == 1 then
                mw, mh = mh, mw
              end
              local width, height = math.min(400, mw - 32), math.min(600, mh - 32)
              local cursor = hl.get_cursor_pos()
              local x = math.max(m.position.x + 16, math.min(cursor.x - width / 2, m.position.x + mw - width - 16))
              local y = math.max(m.position.y + 16, math.min(cursor.y - 24, m.position.y + mh - height - 16))
              hl.dispatch(hl.dsp.window.float({ action = "enable", window = w }))
              hl.dispatch(hl.dsp.window.resize({ x = width, y = height, window = w }))
              hl.dispatch(hl.dsp.window.move({ x = x, y = y, window = w }))
            end'')
        ];
      }
    ];

    window_rule = [
      # Workaround W13 (Plane 262): Hyprland treats every window as
      # maximized and answers a client's unmaximize by maximizing it.
      # Firefox unmaximizes before each resize an extension asks for, so
      # Passbolt's pop-up went maximized on its first resize. Firefox's own
      # maximize (double-click on the empty tab strip) does nothing now;
      # Hyprland's maximize keys still work.
      {
        match.class = "^(firefox)$";
        suppress_event = "maximize";
      }
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
