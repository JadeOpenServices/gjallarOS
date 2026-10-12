-- oddc-quick-keys writes preset1, preset2 or preset3 here when the device
-- reports a change. The file is missing until the first key press after boot,
-- and the widget stays hidden until then. GjallarOS maps none of the keys:
-- each preset sends ODDC's own macro keys, free for the user's binds.
local presetFile = "/run/oddc/quick-keys/preset"

local presets = {
  preset1 = { label = "1" },
  preset2 = { label = "2" },
  preset3 = { label = "3" },
}

local shown = false

function update()
  noctalia.setUpdateInterval(1000)

  local raw = noctalia.readFile(presetFile)
  local preset = raw and presets[string.match(raw, "^%s*(%S+)")]
  if preset == shown then
    return
  end
  shown = preset

  if preset == nil then
    barWidget.setVisible(false)
    return
  end
  barWidget.setGlyph("keyboard")
  barWidget.setText(preset.label)
  barWidget.setTooltip("Quick Keys preset " .. preset.label .. ": keys free for your own shortcuts")
  barWidget.setVisible(true)
end
