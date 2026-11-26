package etcd_test

import (
	"reflect"
	"slices"
	"sync"
	"testing"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/etcd"
	"go.krak3n.dev/foundation/config/etcd/etcdtest"
)

// The provider is the remote layer, and says where it looks without looking.
var (
	_ config.RemoteLayer = (*etcd.Provider)(nil)
	_ config.Describer   = (*etcd.Provider)(nil)
)

func TestLoadsValuesFromKeys(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{
		"/services/shop/svc/http/server/addr":          ":8080",
		"/services/shop/svc/http/server/port":          "8080",
		"/services/shop/svc/http/server/debug":         "true",
		"/services/shop/svc/http/server/timeout":       "30s",
		"/services/shop/svc/http/server/tags":          "a,b,c",
		"/services/shop/svc/http/server/limits":        "x:1,y:2",
		"/services/shop/svc/http/server/tls/cert_file": "/etc/cert.pem",
	}, "/services/shop")

	var got serverConfig

	if err := p.Unmarshal([]string{"svc", "http.server"}, &got); err != nil {
		t.Fatal(err)
	}

	want := serverConfig{
		Addr: ":8080", Port: 8080, Debug: true, Timeout: "30s",
		Tags:   []string{"a", "b", "c"},
		Limits: map[string]int{"x": 1, "y": 2},
		TLS:    tlsConfig{CertFile: "/etc/cert.pem"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAbsentKeysLeaveTheFieldAlone(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/p/svc/port": "9"}, "/p")

	got := serverConfig{Addr: "default", Port: 1, TLS: tlsConfig{CertFile: "default.pem"}}

	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	want := serverConfig{Addr: "default", Port: 9, TLS: tlsConfig{CertFile: "default.pem"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAnEmptyPrefixOfKeysIsNotAnError(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, nil, "/nothing/here")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatalf("nothing stored is normal, got %v", err)
	}
}

func TestAnEmptyValueIsNotSetExceptForAString(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/p/svc/addr": "", "/p/svc/port": "", "/p/svc/debug": ""}, "/p")

	got := serverConfig{Addr: "default", Port: 7, Debug: true}
	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Addr != "" {
		t.Errorf("Addr = %q, an empty string is a value", got.Addr)
	}

	if got.Port != 7 || !got.Debug {
		t.Errorf("an empty value overwrote a default: %+v", got)
	}
}

func TestKeysFollowThePathNotTheCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path []string
		key  string
	}{
		{"one segment", []string{"svc"}, "/p/svc/addr"},
		{"nested", []string{"svc", "http", "server"}, "/p/svc/http/server/addr"},
		{"dot", []string{"svc", "http.server"}, "/p/svc/http/server/addr"},
		{"slash and hyphen", []string{"svc", "http/server-x"}, "/p/svc/http/server/x/addr"},
		{"underscore", []string{"svc", "http_server"}, "/p/svc/http_server/addr"},
		{"case is folded", []string{"Svc", "HTTP"}, "/p/svc/http/addr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, _ := provider(t, map[string]string{tt.key: ":1"}, "/p")

			var got serverConfig
			if err := p.Unmarshal(tt.path, &got); err != nil {
				t.Fatal(err)
			}

			if got.Addr != ":1" {
				t.Errorf("Addr = %q, want the value at %s", got.Addr, tt.key)
			}
		})
	}
}

// A function's name can't take the provider out of its prefix: it is split into
// words on anything but letters, digits and underscores, so no key has a dot
// segment or an empty one.
func TestAPathCannotLeaveThePrefix(t *testing.T) {
	t.Parallel()

	p, s := provider(t, map[string]string{"/p/svc/etc/addr": "inside", "/etc/addr": "outside"}, "/p")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc", "../../etc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Addr != "inside" {
		t.Errorf("Addr = %q", got.Addr)
	}

	for _, tx := range txnCalls(s) {
		for _, k := range requestedKeys(tx) {
			if len(k) < 4 || k[:3] != "/p/" || slices.Contains([]string{"", ".", ".."}, k) {
				t.Errorf("key %q is outside the prefix", k)
			}
		}
	}
}

func TestAKeyOfAnotherPrefixIsNotRead(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/ab/svc/addr": "other", "/a/svc/addr": "mine"}, "/a")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.Addr != "mine" {
		t.Errorf("Addr = %q", got.Addr)
	}

	p2, _ := provider(t, map[string]string{"/ab/svc/addr": "other"}, "/a")

	var none serverConfig
	if err := p2.Unmarshal([]string{"svc"}, &none); err != nil || none.Addr != "" {
		t.Errorf("read %q beyond /a: %v", none.Addr, err)
	}
}

func TestOneTransactionReadsEveryFieldAtOneRevision(t *testing.T) {
	t.Parallel()

	p, s := provider(t, map[string]string{"/p/svc/addr": ":1"}, "/p")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
		t.Fatal(err)
	}

	calls := s.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls, want one transaction: %+v", len(calls), calls)
	}

	txs := txnCalls(s)
	if len(txs) != 1 {
		t.Fatalf("calls = %+v, want one transaction", calls)
	}

	wantKeys := []string{
		"/p/svc/addr", "/p/svc/port", "/p/svc/debug", "/p/svc/timeout", "/p/svc/tags", "/p/svc/limits", "/p/svc/tls/cert_file",
	}

	if got := requestedKeys(txs[0]); !slices.Equal(got, wantKeys) {
		t.Errorf("keys = %v, want %v", got, wantKeys)
	}

	if len(txs[0].Compare) != 0 || len(txs[0].Failure) != 0 || len(txs[0].Success) != len(wantKeys) {
		t.Errorf("transaction = %+v", txs[0])
	}

	for _, op := range txs[0].Success {
		rr := op.GetRequestRange()
		if rr == nil {
			t.Fatalf("operation %+v is not a read", op)
		}

		// Linearizable (not served from a follower's possibly old copy), exactly
		// the key, with its value.
		if rr.Serializable || len(rr.RangeEnd) != 0 || rr.KeysOnly || rr.CountOnly || rr.Limit > 1 || rr.Revision != 0 {
			t.Errorf("read of %q is %+v", rr.Key, rr)
		}
	}
}

func TestOnlyReadsAreMade(t *testing.T) {
	t.Parallel()

	p, s := provider(t, map[string]string{"/p/svc/addr": ":1"}, "/p")

	var got serverConfig

	_ = p.Unmarshal([]string{"svc"}, &got)
	_, _ = p.Describe([]string{"svc"}, &got)

	for _, c := range s.Calls() {
		if c.Method != "/etcdserverpb.KV/Txn" {
			t.Errorf("the provider made a call to %s", c.Method)
		}
	}

	for _, tx := range txnCalls(s) {
		for _, op := range tx.Success {
			if op.GetRequestRange() == nil {
				t.Errorf("the transaction does more than read: %+v", op)
			}
		}
	}
}

func TestNoFieldsMeansNoRequest(t *testing.T) {
	t.Parallel()

	p, s := provider(t, nil, "/p")

	var none struct{ unexported string }

	if err := p.Unmarshal([]string{"svc"}, &none); err != nil {
		t.Fatal(err)
	}

	if n := len(s.Calls()); n != 0 {
		t.Errorf("%d calls for a struct with nothing to read", n)
	}
}

func TestDescribeSaysWhereWithoutLooking(t *testing.T) {
	t.Parallel()

	p, s := provider(t, map[string]string{"/p/svc/http/server/addr": ":1"}, "/p")

	type described struct {
		Addr string    `config:"addr" help:"where to listen"`
		TLS  tlsConfig `config:"tls"`
		Skip string
	}

	var dst described

	keys, err := p.Describe([]string{"svc", "http.server"}, &dst)
	if err != nil {
		t.Fatal(err)
	}

	want := []config.FieldKey{
		{Field: "Addr", Key: "/p/svc/http/server/addr", Provider: "etcd", Help: "where to listen", Type: "string"},
		{Field: "TLS.CertFile", Key: "/p/svc/http/server/tls/cert_file", Provider: "etcd", Type: "string"},
	}

	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %+v, want %+v", keys, want)
	}

	if dst.Addr != "" {
		t.Errorf("Describe changed the struct: %+v", dst)
	}

	if n := len(s.Calls()); n != 0 {
		t.Errorf("Describe made %d calls, it must read nothing", n)
	}
}

func TestTagsOverrideTheCommonName(t *testing.T) {
	t.Parallel()

	type tagged struct {
		Own     string `config:"common" etcd:"own"`
		Common  string `config:"shared"`
		OnlyOpt string `config:"named" etcd:",opt"`
		Skipped string `config:"skipped" etcd:"-"`
		Etcd    string `etcd:"only_etcd"`
		Plain   string
	}

	p, s := provider(t, map[string]string{
		"/p/svc/own": "1", "/p/svc/common": "no", "/p/svc/shared": "2", "/p/svc/named": "3",
		"/p/svc/skipped": "no", "/p/svc/only_etcd": "4",
	}, "/p")

	var got tagged
	err := p.Unmarshal([]string{"svc"}, &got)

	// A tag option the provider doesn't know is a mistake in the struct, reported
	// for the field; the other fields are still set.
	if fe := fieldError(err, "OnlyOpt"); fe == nil {
		t.Errorf("an unknown tag option was accepted: %v", err)
	}

	want := tagged{Own: "1", Common: "2", Etcd: "4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	for _, tx := range txnCalls(s) {
		for _, k := range requestedKeys(tx) {
			if k == "/p/svc/skipped" || k == "/p/svc/common" {
				t.Errorf("read %q, which the struct says to skip", k)
			}
		}
	}
}

// What a tag names becomes a part of a key, so a name that is not a plain word
// (a slash, dots, a space) is a mistake in the struct and never a key.
func TestATagNameThatIsNotAWordIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"../x", "a/b", "a b", "", "a.b", "a\nb", "é", "9a", "a-b"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, s := provider(t, nil, "/p")

			dst := reflect.New(reflect.StructOf([]reflect.StructField{
				{Name: "Bad", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(`etcd:"` + name + `"`)},
				{Name: "Good", Type: reflect.TypeFor[string](), Tag: `config:"good"`},
			})).Interface()

			err := p.Unmarshal([]string{"svc"}, dst)
			if name != "" && fieldError(err, "Bad") == nil {
				t.Errorf("name %q was accepted: %v", name, err)
			}

			for _, tx := range txnCalls(s) {
				for _, k := range requestedKeys(tx) {
					if k != "/p/svc/good" {
						t.Errorf("read %q", k)
					}
				}
			}
		})
	}
}

func TestTwoFieldsOneKeyIsAMistake(t *testing.T) {
	t.Parallel()

	type clash struct {
		A string `config:"same"`
		B string `config:"same"`
	}

	p, _ := provider(t, map[string]string{"/p/svc/same": "x"}, "/p")

	var got clash

	err := p.Unmarshal([]string{"svc"}, &got)
	if fe := fieldError(err, "B"); fe == nil {
		t.Fatalf("the second field reading a key was accepted: %v", err)
	}

	if got.A != "x" {
		t.Errorf("A = %q: the first field still reads its key", got.A)
	}
}

func TestUnsupportedTypeIsReportedAndTheRestLoads(t *testing.T) {
	t.Parallel()

	type bad struct {
		Ch   chan int `config:"ch"`
		Addr string   `config:"addr"`
	}

	p, _ := provider(t, map[string]string{"/p/svc/addr": ":1"}, "/p")

	var got bad

	err := p.Unmarshal([]string{"svc"}, &got)
	if fieldError(err, "Ch") == nil {
		t.Errorf("a channel field was accepted: %v", err)
	}

	if got.Addr != ":1" {
		t.Errorf("Addr = %q, the other fields are still loaded", got.Addr)
	}
}

func TestEveryProblemIsReportedWithItsKey(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{
		"/p/svc/port": "notanumber", "/p/svc/debug": "maybe", "/p/svc/addr": ":ok",
	}, "/p")

	var got serverConfig

	err := p.Unmarshal([]string{"svc"}, &got)

	for field, key := range map[string]string{"Port": "/p/svc/port", "Debug": "/p/svc/debug"} {
		fe := fieldError(err, field)
		if fe == nil {
			t.Errorf("no problem for %s: %v", field, err)

			continue
		}

		if fe.Provider != "etcd" || fe.Key != key {
			t.Errorf("%s: %+v", field, fe)
		}
	}

	if got.Addr != ":ok" {
		t.Errorf("Addr = %q: the fields that are fine are still set", got.Addr)
	}
}

func TestInvalidDestinationsAreErrors(t *testing.T) {
	t.Parallel()

	p, s := provider(t, nil, "/p")

	var nilPtr *serverConfig

	for name, dst := range map[string]any{
		"nil": nil, "nil pointer": nilPtr, "value": serverConfig{}, "pointer to int": new(int), "pointer to pointer": new(*serverConfig),
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Unmarshal([]string{"svc"}, dst); err == nil {
				t.Error("no error")
			}

			if _, err := p.Describe([]string{"svc"}, dst); err == nil {
				t.Error("Describe: no error")
			}
		})
	}

	if n := len(s.Calls()); n != 0 {
		t.Errorf("%d calls for destinations that can't be loaded", n)
	}
}

// A provider is made once and asked again, by every instance of a program that
// restarts and by the trial run beside it.
func TestTheSameAnswerEveryTime(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/p/svc/addr": ":1", "/p/svc/port": "2"}, "/p")

	var first, second serverConfig

	if err := p.Unmarshal([]string{"svc"}, &first); err != nil {
		t.Fatal(err)
	}

	// The same struct again, and a fresh one, are no clash.
	if err := p.Unmarshal([]string{"svc"}, &first); err != nil {
		t.Fatalf("asked again: %v", err)
	}

	if err := p.Unmarshal([]string{"svc"}, &second); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("%+v then %+v", first, second)
	}
}

func TestSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{"/p/svc/addr": ":1", "/p/svc/port": "2"}, "/p")

	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			var got serverConfig

			if err := p.Unmarshal([]string{"svc"}, &got); err != nil || got.Addr != ":1" || got.Port != 2 {
				t.Errorf("got %+v, %v", got, err)
			}

			if _, err := p.Describe([]string{"svc"}, &got); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()
}

func TestAClosedClientIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	c := s.Client(t)
	p := etcd.New(c, "/p")

	_ = c.Close()

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err == nil {
		t.Error("no error from a closed client")
	}
}

func TestNoClientIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	p := etcd.New(nil, "/p")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err == nil {
		t.Error("no error without a client")
	}
}

// Another key's text that happens to contain "etcd:" is no mistake in the etcd tag,
// and a repeated etcd key is.
func TestTheTagIsReadAsReflectReadsIt(t *testing.T) {
	t.Parallel()

	// Built at run time: staticcheck refuses the repeated key in source.
	dst := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "Help", Type: reflect.TypeFor[string](), Tag: `config:"help" help:"read from etcd: here"`},
		{Name: "Twice", Type: reflect.TypeFor[string](), Tag: `config:"twice" etcd:"x" etcd:"-"`},
	}))

	p, _ := provider(t, map[string]string{"/p/svc/help": "ok"}, "/p")

	err := p.Unmarshal([]string{"svc"}, dst.Interface())

	if fieldError(err, "Help") != nil {
		t.Errorf("a help text with etcd: in it is refused: %v", err)
	}

	if fieldError(err, "Twice") == nil {
		t.Errorf("a repeated etcd key was accepted: %v", err)
	}

	if got := dst.Elem().FieldByName("Help").String(); got != "ok" {
		t.Errorf("Help = %q", got)
	}
}
