> **History.** This is the original design idea for command line flags, kept as written. [CONFIG.md](../CONFIG.md) replaced it and is the reference.
>
> **Status.** The concept below led to a different grammar, now built (stage 1): `binaryName --a --string abc`, with markers instead of sub commands, and `config/flags` as the provider. See [CONFIG.md](../CONFIG.md#command-line-flags) ("Command line flags") for what exists. `f.Help`, the `help` tag and `--help` (stage 2) are built too: see [CONFIG.md](../CONFIG.md#help) ("Help").

# CLI Flag Support Concept

Ideally this would work similar to that of Cobra. Only functions which expose config with explicit opt-in for flags would be displayed via --help.

If --help is called from root this should list the functions (1 level deep), that expose flags with its own help text.

## Example

```go
package main

import (
	"log/slog"
	"os"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config/env"
)

type ConfigA struct {
	String string  `env:"STRING" flag:"string" help:"Names thing"`
	Num    int     `env:"NUM"`
	Bool   bool    `env:"BOOL"`
	Float  float64 `env:"FLOAT"`
} `help:"Short help or description"`

type ConfigB struct {
	String string  `env:"STRING"`
	Num    int     `env:"NUM"`
	Bool   bool    `env:"BOOL"`
	Float  float64 `env:"FLOAT"`
} 

func main() {
	foundation.Run("root", func(f *foundation.F) {
        f.Help("Long help message")

		f.Run("a", func(f *foundation.F) {
            f.Help("A help description")

			var cfg ConfigA

			f.Load(&cfg, env.New(), flags.NewProider())
		})

		f.Run("b", func(f *foundation.F) {
			var cfg ConfigB

			f.Load(&cfg, env.New(), flags.New())
		})
	})
}
```

Flag usage would look like:

```sh
binaryName --help

Long help message

Commands:

a Short help or description
```

```sh
binaryName a --help

Long A help message

Options:

--string Names a thing
```
