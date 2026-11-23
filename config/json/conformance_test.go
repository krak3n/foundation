package json_test

import (
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/json"
)

var (
	_ config.Provider = (*json.FileProvider)(nil)
	_ config.Provider = (*json.RawProvider)(nil)

	_ config.Checker = (*json.FileProvider)(nil)
	_ config.Checker = (*json.RawProvider)(nil)
)
