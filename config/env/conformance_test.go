package env_test

import (
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
)

var (
	_ config.Provider = (*env.Provider)(nil)
	_ config.Checker  = (*env.Provider)(nil)
)
