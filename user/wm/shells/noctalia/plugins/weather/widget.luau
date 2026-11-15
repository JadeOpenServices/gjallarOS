-- The installer guesses the weather city from the timezone. Home Manager
-- writes the city the weather uses to gjallar/weather-address (empty when
-- the weather is off); gjallar-weather-city writes the one the user agreed
-- to. The pin shows until both match, and a click asks.
local home = noctalia.getenv("HOME") or ""
local configHome = noctalia.getenv("XDG_CONFIG_HOME") or (home .. "/.config")
local stateHome = noctalia.getenv("XDG_STATE_HOME") or (home .. "/.local/state")
local addressFile = configHome .. "/gjallar/weather-address"
local confirmedFile = stateHome .. "/gjallar/weather-confirmed"

local shown = nil

function update()
  noctalia.setUpdateInterval(5000)

  local address = noctalia.readFile(addressFile)
  local confirmed = noctalia.readFile(confirmedFile)
  local tooltip = ""
  if address ~= nil and address ~= confirmed then
    if address == "" then
      tooltip = "The weather needs your city"
    else
      tooltip = "Weather for " .. address .. ". Is that your city?"
    end
  end
  if tooltip == shown then
    return
  end
  shown = tooltip

  if tooltip == "" then
    barWidget.setVisible(false)
    return
  end
  barWidget.setGlyph("map-pin-question")
  barWidget.setTooltip(tooltip)
  barWidget.setVisible(true)
end

function onClick()
  noctalia.runAsync("systemctl --user start gjallar-weather-city.service")
end
