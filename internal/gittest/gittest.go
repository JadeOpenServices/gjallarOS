// Package gittest keeps the git commands that tests start from working on
// in the background after a test ends.
package gittest

import "os"

// NoBackgroundMaintenance turns off git's automatic gc and maintenance for
// every git the test binary starts. commit, fetch, merge and pull otherwise
// start "git maintenance run --auto --detach", which can still write into a
// repository while the test removes its temporary directory ("directory not
// empty"). Call it from TestMain before m.Run.
func NoBackgroundMaintenance() {
	for key, value := range map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "gc.auto",
		"GIT_CONFIG_VALUE_0": "0",
		"GIT_CONFIG_KEY_1":   "maintenance.auto",
		"GIT_CONFIG_VALUE_1": "false",
	} {
		os.Setenv(key, value)
	}
}
