-- gjallar-virtual-keyboard (user/wm/hyprland/touch.nix) writes on or off here
-- when the tablet mode switch flips, and none on a touchscreen without one.
-- With a keyboard attached the button stays out of the way.
local stateFile = (noctalia.getenv("XDG_RUNTIME_DIR") or "") .. "/gjallar/tablet-mode"

local shown = nil

function update()
  noctalia.setUpdateInterval(1000)

  local raw = noctalia.readFile(stateFile)
  local state = raw and string.match(raw, "^%s*(%S+)")
  local visible = state == "on" or state == "none"
  if visible == shown then
    return
  end
  shown = visible

  barWidget.setGlyph("keyboard")
  barWidget.setTooltip("Show or hide on-screen keyboard")
  barWidget.setVisible(visible)
end

function onClick()
  noctalia.runAsync("gjallar-virtual-keyboard toggle")
end
