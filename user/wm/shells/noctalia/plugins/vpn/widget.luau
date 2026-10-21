-- gjallar-vpn-trust (apps/tailscale/nixos.nix) writes its decision here on
-- every network change. The shield shows while the VPN blocks this network
-- or a pause lets it through; a click pauses or ends the pause
-- (apps/tailscale/home.nix).
local statusFile = "/run/gjallar/vpn-trust.json"

local blockedText = {
  ["exit-node-offline"] = "the exit node is offline",
  ["no-exit-node"] = "no exit node can be set",
  ["tailscale-down"] = "Tailscale is not running",
}

local shown = false
local paused = false

function update()
  noctalia.setUpdateInterval(2000)

  local raw = noctalia.readFile(statusFile) or ""
  -- The SSID is display text only; Wi-Fi names with quotes show as
  -- "this network".
  local ssid = string.match(raw, '"ssid"%s*:%s*"([^"\\]*)"')
  local where = ssid and ("Wi-Fi " .. ssid) or "this network"
  local reason = string.match(raw, '"blocked"%s*:%s*"([^"]*)"')
  local tooltip
  paused = string.find(raw, '"paused"%s*:%s*{') ~= nil
  if paused then
    -- The bar tooltip cuts off near 90 characters.
    tooltip = "VPN paused on " .. where .. ": no tunnel. Click to end the pause."
  elseif reason then
    tooltip = "VPN blocks " .. where .. ": " .. (blockedText[reason] or reason) .. ". Click to pause."
  end
  if tooltip == shown then
    return
  end
  shown = tooltip

  if tooltip == nil then
    barWidget.setVisible(false)
    return
  end
  barWidget.setGlyph(paused and "shield-off" or "shield-lock")
  barWidget.setTooltip(tooltip)
  barWidget.setVisible(true)
end

function onClick()
  if paused then
    noctalia.runAsync("systemctl --user start vpn-resume.service")
  else
    noctalia.runAsync("systemctl --user start vpn-pause.service")
  end
end
