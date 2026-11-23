package foundation_test

//lint:file-ignore SA5008 the structs here repeat a tag key on purpose: it is what is under test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/env"
	"go.krak3n.dev/foundation/config/flags"
	"go.krak3n.dev/foundation/config/json"
	"go.krak3n.dev/foundation/foundationtest"
)

// A key given twice in a struct tag (`config:"addr" config:"required"`) is read as
// its first value, so the second is dropped without a word and a field the program
// meant to require is optional. Reading the struct is where that is caught, in a
// run and in a dry run, for the keys the framework reads.

type (
	requiredTwice struct {
		Addr string `config:"addr" config:"required"`
	}

	nameTwice struct {
		Addr string `config:"addr" config:"address"`
	}

	envTwice struct {
		Addr string `env:"ADDR" env:"ADDRESS"`
	}

	flagTwice struct {
		Addr string `flag:"addr" flag:"address"`
	}

	jsonTwice struct {
		Addr string `json:"addr" json:"address"`
	}

	nestedTwice struct {
		TLS struct {
			CertFile string `config:"cert_file" config:"required"`
		} `config:"tls"`
	}

	embeddedInner struct {
		Addr string `config:"addr" config:"required"`
	}

	embeddedTwice struct {
		embeddedInner
	}
)

// loadStruct runs a program that loads T, and returns the exit codes and the
// configuration problems it logged.
func loadStruct[T any](t *testing.T, opts ...foundation.Option) ([]int, []logged) {
	t.Helper()

	return loadDst(t, func() any { return new(T) }, opts...)
}

// loadDst is loadStruct for a struct made at run time.
func loadDst(t *testing.T, dst func() any, opts ...foundation.Option) ([]int, []logged) {
	t.Helper()

	exit := installExit(t)

	recorder := foundationtest.NewLogRecorder()
	recorder.Install(t)

	foundation.Run("svc", func(f *foundation.F) {
		f.Load(dst())
	}, opts...)

	return exit.Codes(), problemsLogged(recorder)
}

// withTag makes a struct with one string field, Addr, with exactly this tag. It is
// made at run time because a tag that can't be read is what go vet refuses to
// have in source.
func withTag(tag string) func() any {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "Addr", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(tag)},
	})

	return func() any { return reflect.New(typ).Interface() }
}

func TestARepeatedTagKeyIsAProblemWithTheField(t *testing.T) {
	// Options are made for each run: a flags provider remembers the names it was
	// given, so one shared by two runs would say its own flags clash.
	envOnly := func() []foundation.Option {
		return []foundation.Option{foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{})))}
	}

	tests := map[string]struct {
		run   func(t *testing.T, opts ...foundation.Option) ([]int, []logged)
		opts  func() []foundation.Option
		field string
		key   string
	}{
		"config, which also says required": {loadStruct[requiredTwice], envOnly, "Addr", "config"},
		"config, which names the field":    {loadStruct[nameTwice], envOnly, "Addr", "config"},
		"env":                              {loadStruct[envTwice], envOnly, "Addr", "env"},
		"flag": {loadStruct[flagTwice], func() []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(flags.New(flags.WithArgs([]string{})))}
		}, "Addr", "flag"},
		"json": {loadStruct[jsonTwice], func() []foundation.Option {
			return []foundation.Option{foundation.WithConfigProviders(json.Raw([]byte(`{}`)))}
		}, "Addr", "json"},
		"config in a nested struct":    {loadStruct[nestedTwice], envOnly, "CertFile", "config"},
		"config in an embedded struct": {loadStruct[embeddedTwice], envOnly, "Addr", "config"},
	}

	for name, tt := range tests {
		for mode, extra := range map[string][]foundation.Option{
			"a run":     nil,
			"a dry run": {foundation.WithDryRun(true)},
		} {
			t.Run(name+" in "+mode, func(t *testing.T) {
				codes, problems := tt.run(t, append(tt.opts(), extra...)...)

				assertExitCodesEqual(t, codes, 1)

				var found bool

				for _, p := range problems {
					if !strings.Contains(p.attrs["field"], tt.field) {
						continue
					}

					found = true

					if !strings.Contains(p.attrs["err"], `"`+tt.key+`"`) || !strings.Contains(p.attrs["err"], "more than once") {
						t.Errorf("the problem says %q, want it to name %q and say it is given more than once", p.attrs["err"], tt.key)
					}
				}

				if !found {
					t.Errorf("no problem was reported for %s: %v", tt.field, problems)
				}
			})
		}
	}
}

// What the tag held is a name in code and not a value, but the reason is still
// only a reason.
func TestTheProblemWithARepeatedKeyHasNoValueInIt(t *testing.T) {
	type secretTags struct {
		Pass string `config:"canary_secret_9d2e" config:"canary_secret_1f3a"`
	}

	_, problems := loadStruct[secretTags](t,
		foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{}))))

	if len(problems) == 0 {
		t.Fatal("no problem was reported")
	}

	for _, p := range problems {
		if strings.Contains(p.attrs["err"], "canary") {
			t.Errorf("the problem says %q", p.attrs["err"])
		}
	}
}

func TestATagThatCannotBeReadToTheEndIsAProblemWithTheField(t *testing.T) {
	for name, tag := range map[string]string{
		"a quote that is never closed":          `config:"addr,required`,
		"a quote never closed after a good key": `json:"addr" config:"addr,required`,
		"a value that is not quoted":            `config:addr`,
	} {
		t.Run(name, func(t *testing.T) {
			codes, problems := loadDst(t, withTag(tag),
				foundation.WithConfigProviders(env.New(env.WithMap(map[string]string{}))))

			assertExitCodesEqual(t, codes, 1)

			var found bool

			for _, p := range problems {
				if strings.Contains(p.attrs["field"], "Addr") && strings.Contains(p.attrs["err"], "malformed") {
					found = true
				}
			}

			if !found {
				t.Errorf("no problem about the malformed tag of Addr: %v", problems)
			}
		})
	}
}

// Every provider that reads a struct says so, whatever the framework does with it
// after.
func TestEveryProviderReportsARepeatedKeyAsAFieldProblem(t *testing.T) {
	tests := map[string]struct {
		provider config.Provider
		dst      any
	}{
		"env":                      {env.New(env.WithMap(map[string]string{})), &envTwice{}},
		"env, by the common name":  {env.New(env.WithMap(map[string]string{})), &nameTwice{}},
		"json":                     {json.Raw([]byte(`{}`)), &jsonTwice{}},
		"json, by the common name": {json.Raw([]byte(`{}`)), &nameTwice{}},
		"flags":                    {flags.New(flags.WithArgs([]string{})), &flagTwice{}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := tt.provider.Unmarshal([]string{"svc"}, tt.dst)

			var fe *config.FieldError

			if !errors.As(err, &fe) {
				t.Fatalf("Unmarshal = %v, want a problem with the field", err)
			}

			if fe.Field != "Addr" || !strings.Contains(fe.Err.Error(), "more than once") {
				t.Errorf("the problem is %+v", fe)
			}
		})
	}
}

// What is fine stays fine, so the check can't refuse a struct that was never wrong.
func TestTagsThatAreFineAreStillFine(t *testing.T) {
	type (
		required struct {
			Addr string `config:"addr,required"`
		}

		unrelatedTwice struct {
			Addr string `config:"addr" validate:"x" validate:"y"`
		}

		manyKeys struct {
			Addr string `config:"addr" env:"ADDR" flag:"addr,short=a" help:"where to listen" json:"addr"`
		}
	)

	vars := env.New(env.WithMap(map[string]string{"SVC_ADDR": ":9"}))

	t.Run("a tag that is cut off, with nothing of ours in it", func(t *testing.T) {
		codes, problems := loadDst(t, withTag(`config:"addr" validate:"x`), foundation.WithConfigProviders(vars))

		if len(codes) != 0 || len(problems) != 0 {
			t.Errorf("exit codes %v, problems %v: want none", codes, problems)
		}
	})

	for name, run := range map[string]func(*testing.T, ...foundation.Option) ([]int, []logged){
		"the usual required tag":          loadStruct[required],
		"a repeated key that is not ours": loadStruct[unrelatedTwice],
		"one of each key":                 loadStruct[manyKeys],
	} {
		t.Run(name, func(t *testing.T) {
			codes, problems := run(t, foundation.WithConfigProviders(vars))

			if len(codes) != 0 || len(problems) != 0 {
				t.Errorf("exit codes %v, problems %v: want none", codes, problems)
			}
		})
	}
}

func assertExitCodesEqual(t *testing.T, got []int, want ...int) {
	t.Helper()

	if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
		t.Fatalf("exit codes = %v, want %v", got, want)
	}
}
