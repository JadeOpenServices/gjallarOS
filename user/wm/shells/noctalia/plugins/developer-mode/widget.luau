-- Developer mode is on whenever this plugin is enabled
-- (user/wm/shells/noctalia/default.nix). The tooltip says whether the
-- running system holds local files, from the record the build left in
-- /etc/gjallar/build-source.json; a click shows gjallarctl system status.
local buildSource = "/etc/gjallar/build-source.json"

local shown = nil

function update()
  noctalia.setUpdateInterval(10000)

  local raw = noctalia.readFile(buildSource) or ""
  -- builtins.toJSON sorts the keys, so the file list comes first; file
  -- names hold no quotes (internal/localchanges).
  local hash = string.match(raw, '"local"%s*:%s*{.-"hash"%s*:%s*"(%x+)"')
  local tooltip
  if hash then
    tooltip = "Developer mode is on. This system runs local Nix files ("
      .. string.sub(hash, 1, 12)
      .. "). Click for the status."
  else
    tooltip = "Developer mode is on. This system runs no local files yet. Click for the status."
  end
  if tooltip == shown then
    return
  end
  shown = tooltip

  barWidget.setGlyph("code")
  barWidget.setTooltip(tooltip)
  barWidget.setVisible(true)
end

function onClick()
  noctalia.runAsync("systemctl --user start developer-mode-status.service")
end
