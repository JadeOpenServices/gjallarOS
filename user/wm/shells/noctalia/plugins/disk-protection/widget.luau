-- gjallarctl luks remind (system/security/disk-unlock.nix) writes what is
-- missing here at login and after every keyslot change. The shield stays
-- until it reads done; a click starts the setup in a terminal. The PIN
-- suggestion ("pin") is a login card only and shows no shield.
local stateFile = (noctalia.getenv("XDG_RUNTIME_DIR") or "") .. "/gjallar/disk-protection"

local tooltips = {
  ["setup"] = "Disk protection not finished. Click to set it up.",
  ["secure-boot"] = "Disk protection not set up: Secure Boot is off. Click to set it up.",
  ["recovery-key"] = "No recovery key for your disk. Click to create one.",
}

local shown = false

function update()
  noctalia.setUpdateInterval(2000)

  local raw = noctalia.readFile(stateFile)
  local tooltip = raw and tooltips[string.match(raw, "^%s*(%S+)")]
  if tooltip == shown then
    return
  end
  shown = tooltip

  if tooltip == nil then
    barWidget.setVisible(false)
    return
  end
  barWidget.setGlyph("shield-exclamation")
  barWidget.setTooltip(tooltip)
  barWidget.setVisible(true)
end

function onClick()
  noctalia.runAsync("systemctl --user start disk-protection-setup.service")
end
