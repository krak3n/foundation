# Foundation YAML

Reads [Foundation](../../README.md) configuration from YAML files and directories. It is a module of its own, so the core stays free of dependencies, and it accepts a strict subset of YAML that says what it means: a configuration file is untrusted input, so a file that could say something other than what a reviewer sees is refused.

- **Pre-1.0.** The API may still change.
- **Requires Go 1.26 or newer.**
- **One dependency**, `go.yaml.in/yaml/v3`, used only to build a tree of nodes. It never decodes a value.

## Quick start

```sh
go get go.krak3n.dev/foundation/config/yaml
```

```go
package main

import (
	"log/slog"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/yaml"
)

// Config is what the "api" function loads. A field is named once, in its config tag,
// and that is the key in the file.
type Config struct {
	Addr    string        `config:"addr"`
	Timeout time.Duration `config:"timeout"`
}

func main() {
	foundation.Run("shop", func(f *foundation.F) {
		f.Run("api", func(f *foundation.F) {
			// These are the defaults: the file changes them, and the environment
			// (SHOP_API_ADDR) wins over the file.
			cfg := Config{Addr: "127.0.0.1:8080", Timeout: 30 * time.Second}

			f.Load(&cfg)

			f.Log().Info("loaded", slog.String("addr", cfg.Addr), slog.Duration("timeout", cfg.Timeout))
		})
		// The operator chooses the file by naming it in SHOP_CONFIG, and it must exist.
	}, foundation.WithDocumentConfig(yaml.FileFromEnv("SHOP_CONFIG", config.WithRequired())))
}
```

Put the settings in `config.yaml`. The keys are the path of the function that loads them, so `api` inside `shop`:

```yaml
shop:
  api:
    addr: 127.0.0.1:9090
    timeout: 5s
```

Run it, naming the file by its absolute path. The environment wins over the file.

```sh
SHOP_CONFIG="$PWD/config.yaml" go run .                          # addr 127.0.0.1:9090
SHOP_CONFIG="$PWD/config.yaml" SHOP_API_ADDR=127.0.0.1:7070 go run .   # addr 127.0.0.1:7070
```

## Choosing the file

YAML is not in the defaults. You add it to the document layer with `foundation.WithDocumentConfig`, and the environment and flags layers still sit above it.

- `yaml.File("/etc/shop/config.yaml")` reads the path you give. Prefer an absolute one: a relative path depends on where the program starts, which may be somewhere someone else can write. An empty path is an error, never a file that is skipped.
- `yaml.FileFromEnv("SHOP_CONFIG")` reads the file whose path is in the variable you name, so the operator chooses it. With `config.WithRequired()` an unset variable or a missing file stops startup. Errors say `$SHOP_CONFIG`, never the path.
- `yaml.Dir("/etc/shop/conf.d")` and `yaml.DirFromEnv("SHOP_CONFIGS")` read every `.yaml` and `.yml` file in a directory, in name order, later files winning.
- `yaml.Raw(data)` reads bytes, which is useful in tests.

A missing file is skipped unless `config.WithRequired()` is given. On Unix, a file that the group or others can write is refused, unless the program says otherwise in code with `config.WithoutPermissionCheck()`; it is never read from the environment.

## How names are read

A field is named once, by the first part of its `config` tag, and that is its key in the file (`yaml:"..."` overrides it for this provider alone, and `yaml:"-"` skips the field). The keys of the path are nested mappings, matched without regard to case. Only keys that are present are set, and an unquoted `null`, `~` or an empty value counts as not present.

A value is read as the text it was written as, never as what YAML 1.1 would make of it: `no`, `on`, `0755` and `1_000` are strings where a string is wanted. A bool is exactly `true` or `false`. Put secrets in the environment or a secret file, not in the configuration file.

## What it refuses

These are refused wherever they are in the file, even in parts that nothing reads, because each lets a file say something other than what a reviewer sees, or costs more than it should:

- anchors and aliases, which let a few lines expand to gigabytes,
- merge keys and tags,
- complex keys, which are keys that are not a scalar,
- more than one document in a file,
- control characters and text that is not UTF-8, and
- a key that is read and appears twice, exactly or differing only in case.

The limits are 8 MiB for a file, 64 levels of nesting and 100,000 nodes. An error says the line and what kind of mistake it is, and never repeats what the file says, since a configuration file may hold a password in the wrong place.

## Documentation

- [Configuration](../../CONFIG.md#yaml-configyaml): the layers, naming, strict mode and the rules for files.
- [Quick start program](example/quickstart) and [more examples](example): a directory of files, and the environment over a file.
- [Security policy](../../SECURITY.md)

## License

Apache-2.0. See [LICENSE](../../LICENSE).
