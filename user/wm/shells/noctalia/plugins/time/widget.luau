-- gjallarctl system time-watch (user unit gjallar-time-watch) writes the
-- kind on the first line and the tooltip on the second. The clock shows
-- while NTS has not confirmed the time, or the home router sets it; a
-- click shows gjallarctl system time.
local stateFile = (noctalia.getenv("XDG_RUNTIME_DIR") or "") .. "/gjallar/time"

local glyphs = {
  ["unconfirmed"] = "clock-exclamation",
  ["router"] = "clock-question",
}

local shown = nil

function update()
  noctalia.setUpdateInterval(2000)

  local raw = noctalia.readFile(stateFile) or ""
  local kind, tooltip = string.match(raw, "^(%S+)\n([^\n]*)")
  local glyph = kind and glyphs[kind]
  local key = glyph and (glyph .. tooltip) or ""
  if key == shown then
    return
  end
  shown = key

  if glyph == nil then
    barWidget.setVisible(false)
    return
  end
  barWidget.setGlyph(glyph)
  barWidget.setTooltip(tooltip)
  barWidget.setVisible(true)
end

function onClick()
  noctalia.runAsync("systemctl --user start gjallar-time-details.service")
end
