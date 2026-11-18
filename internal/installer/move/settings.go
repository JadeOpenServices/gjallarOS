package move

import (
	"context"
	"encoding/json"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// Settings asks the helper for the old disk's settings (config op) and runs
// the helper's checks on them again (oldroot.CheckConfig), so the new
// install never builds from values the helper alone vouched for. Installer
// keys come from a fresh install. In bring-over the computer is a different
// one: every device key comes from a fresh install as well, except the ones
// in config.BringOverKeeps (L142), and the merged settings pass the same
// checks again, so a longer keep list can never pair values the schema
// refuses (G5).
func (f Flow) Settings(ctx context.Context) (config.User, error) {
	var res movehelper.ConfigResult
	if err := f.Helper.Run(ctx, movehelper.Request{Op: "config"}, &res); err != nil {
		return config.User{}, f.refused(err)
	}
	raw, err := json.Marshal(res.User)
	if err != nil {
		return config.User{}, err
	}
	user, err := oldroot.CheckConfig(raw)
	if err != nil {
		return config.User{}, f.refused(err)
	}
	if f.Mode != BringOver {
		return user, nil
	}
	fresh, err := config.Defaults()
	if err != nil {
		return config.User{}, err
	}
	merged, err := config.WithDeviceKeysFrom(user, fresh, config.BringOverKeeps)
	if err != nil {
		return config.User{}, err
	}
	raw, err = json.Marshal(merged)
	if err != nil {
		return config.User{}, err
	}
	merged, err = oldroot.CheckConfig(raw)
	if err != nil {
		return config.User{}, f.refused(err)
	}
	return merged, nil
}
