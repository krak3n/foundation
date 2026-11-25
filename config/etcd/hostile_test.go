package etcd_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.krak3n.dev/foundation/config/etcd"
	"go.krak3n.dev/foundation/config/etcd/etcdtest"
)

const canary = "canary-s3cr3t-value"

// The prefix is the one thing the program chooses that is also a part of every
// key. A mistake in it stops startup, and the error never repeats it.
func TestABadPrefixIsRefused(t *testing.T) {
	t.Parallel()

	long := "/" + strings.Repeat("a", etcd.MaxPrefixBytes)

	tests := map[string]string{
		"empty":               "",
		"root only":           "/",
		"no leading slash":    "services/shop",
		"trailing slash":      "/services/shop/",
		"empty segment":       "/services//shop",
		"dot segment":         "/services/./shop",
		"dot dot segment":     "/services/../" + canary,
		"newline":             "/services\n/" + canary,
		"NUL":                 "/services\x00/" + canary,
		"control":             "/services\x1b[31m/" + canary,
		"tab":                 "/services\t/" + canary,
		"DEL":                 "/services\x7f/" + canary,
		"invalid UTF-8":       "/services/\xff\xfe" + canary,
		"unicode separator":   "/services /" + canary,
		"format character":    "/services\u202e/" + canary,
		"too long":            long + canary,
		"range end character": "/services/" + canary + "\xff",
	}

	for name, prefix := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, s := provider(t, nil, prefix)

			var got serverConfig

			err := p.Unmarshal([]string{"svc"}, &got)
			if err == nil {
				t.Fatal("Unmarshal accepted the prefix")
			}

			noLeak(t, err, canary, prefix)

			_, err = p.Describe([]string{"svc"}, &got)
			if err == nil {
				t.Error("Describe accepted the prefix")
			}

			noLeak(t, err, canary, prefix)

			if n := len(s.Calls()); n != 0 {
				t.Errorf("%d calls with a bad prefix", n)
			}
		})
	}
}

func TestAGoodPrefixIsAccepted(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{"/a", "/services/shop", "/Services/Shop-1/v2", "/ünï/çode", "/" + strings.Repeat("a", etcd.MaxPrefixBytes-1)} {
		p, _ := provider(t, nil, prefix)

		var got serverConfig
		if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
}

func TestAFieldCountOverTheLimitIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	p, s := provider(t, nil, "/p")

	if err := p.Unmarshal([]string{"svc"}, manyFields(etcd.MaxFields)); err != nil {
		t.Fatalf("%d fields: %v", etcd.MaxFields, err)
	}

	calls := len(s.Calls())

	err := p.Unmarshal([]string{"svc"}, manyFields(etcd.MaxFields+1))
	if err == nil {
		t.Fatal("more fields than the limit were read")
	}

	if n := len(s.Calls()); n != calls {
		t.Errorf("%d calls were made for too many fields", n-calls)
	}
}

func TestATimeoutOutsideTheBoundsIsRefused(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second, etcd.MinTimeout - 1, etcd.MaxTimeout + 1, 1 << 62} {
		p, s := provider(t, nil, "/p", etcd.WithTimeout(d))

		var got serverConfig
		if err := p.Unmarshal([]string{"svc"}, &got); err == nil {
			t.Errorf("timeout %v was accepted", d)
		}

		if n := len(s.Calls()); n != 0 {
			t.Errorf("timeout %v: %d calls", d, n)
		}
	}

	for _, d := range []time.Duration{etcd.MinTimeout, etcd.MaxTimeout, time.Second} {
		p, _ := provider(t, nil, "/p", etcd.WithTimeout(d))

		var got serverConfig
		if err := p.Unmarshal([]string{"svc"}, &got); err != nil {
			t.Errorf("timeout %v: %v", d, err)
		}
	}
}

// A server that never answers can't hold startup: it is an error after the
// deadline, and the request is cancelled.
func TestAServerThatNeverAnswersTimesOut(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{})

	p, _ := hostile(t, "/p", func(ctx context.Context, _ *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
		<-ctx.Done()
		close(cancelled)

		return nil, ctx.Err()
	}, etcd.WithTimeout(200*time.Millisecond))

	start := time.Now()

	var got serverConfig

	err := p.Unmarshal([]string{"svc"}, &got)
	if err == nil {
		t.Fatal("no error from a server that never answered")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a deadline", err)
	}

	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Error("the request was not cancelled")
	}
}

func TestAServerThatIsGoneIsAnError(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	p := etcd.New(s.Client(t), "/p", etcd.WithTimeout(500*time.Millisecond))

	s.Close()

	var got serverConfig

	err := p.Unmarshal([]string{"svc"}, &got)
	if err == nil {
		t.Fatal("no error from a server that is gone")
	}

	noLeak(t, err, s.Endpoint(), "127.0.0.1")

	if !strings.Contains(err.Error(), "unavailable") && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %q, want a reason: unavailable or timed out", err)
	}
}

// An error from the server or the client can name an address, a key or a user.
// None of it is passed on: the provider says what kind of thing went wrong.
func TestErrorsFromTheServerAreReasonsOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"permission denied", status.Error(codes.PermissionDenied, "etcdserver: permission denied: "+canary), "permission denied"},
		{"unauthenticated", status.Error(codes.Unauthenticated, "etcdserver: invalid auth token "+canary), "not authenticated"},
		{"unavailable", status.Error(codes.Unavailable, "dial tcp "+canary+":2379: connection refused"), "unavailable|timed out"},
		{"deadline", status.Error(codes.DeadlineExceeded, canary), "timed out"},
		{"resource exhausted", status.Error(codes.ResourceExhausted, "etcdserver: request is too large "+canary), "limit"},
		{"internal", status.Error(codes.Internal, canary), "failed"},
		{"unknown", errors.New(canary), "failed"},
		{"data loss", status.Error(codes.DataLoss, canary), "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, s := hostile(t, "/p", func(context.Context, *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
				return nil, tt.err
			}, etcd.WithTimeout(2*time.Second))

			var got serverConfig

			err := p.Unmarshal([]string{"svc"}, &got)
			if err == nil {
				t.Fatal("no error")
			}

			noLeak(t, err, canary, s.Endpoint(), "127.0.0.1", "/p/svc")

			// The client retries a read that found the server unavailable until the
			// deadline, so that case may end as a timeout.
			said := false

			for _, want := range strings.Split(tt.want, "|") {
				said = said || strings.Contains(err.Error(), want)
			}

			if !said {
				t.Errorf("err = %q, want it to say %q", err, tt.want)
			}

			if !strings.HasPrefix(err.Error(), "etcd: ") {
				t.Errorf("err = %q, want it to start with etcd: ", err)
			}

			if got.Addr != "" {
				t.Errorf("a failed read changed the struct: %+v", got)
			}
		})
	}
}

// A server (or a machine in the middle of a connection that is not protected by
// TLS) that answers wrongly is an error, and nothing from the answer is used.
func TestAWrongAnswerFailsClosed(t *testing.T) {
	t.Parallel()

	rangeOp := func(kvs ...*mvccpb.KeyValue) *etcdserverpb.ResponseOp {
		return &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{ResponseRange: &etcdserverpb.RangeResponse{Kvs: kvs, Count: int64(len(kvs))}}}
	}

	tests := map[string]func(*etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse{
		"not succeeded": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, map[string]string{"/p/svc/addr": canary})
			resp.Succeeded = false

			return resp
		},
		"fewer answers than questions": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, map[string]string{"/p/svc/addr": canary})
			resp.Responses = resp.Responses[:1]

			return resp
		},
		"more answers than questions": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, map[string]string{"/p/svc/addr": canary})
			resp.Responses = append(resp.Responses, rangeOp())

			return resp
		},
		"no answers": func(*etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			return &etcdserverpb.TxnResponse{Succeeded: true}
		},
		"an answer that is not a read": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, map[string]string{"/p/svc/addr": canary})
			resp.Responses[0] = &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponsePut{ResponsePut: &etcdserverpb.PutResponse{}}}

			return resp
		},
		"an empty answer": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, map[string]string{"/p/svc/addr": canary})
			resp.Responses[0] = &etcdserverpb.ResponseOp{}

			return resp
		},
		"a key that was not asked for": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)
			resp.Responses[0] = rangeOp(&mvccpb.KeyValue{Key: []byte("/p/other/addr"), Value: []byte(canary)})

			return resp
		},
		"a key in another place": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)
			resp.Responses[0] = rangeOp(&mvccpb.KeyValue{Key: []byte("/p/svc/port"), Value: []byte(canary)})

			return resp
		},
		"the same key twice": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)
			kv := &mvccpb.KeyValue{Key: []byte("/p/svc/addr"), Value: []byte(canary)}
			resp.Responses[0] = rangeOp(kv, kv)

			return resp
		},
		"two keys for one": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)
			resp.Responses[0] = rangeOp(
				&mvccpb.KeyValue{Key: []byte("/p/svc/addr"), Value: []byte(canary)},
				&mvccpb.KeyValue{Key: []byte("/p/svc/addr2"), Value: []byte(canary)},
			)

			return resp
		},
		"a nil pair": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)
			resp.Responses[0] = rangeOp(nil)

			return resp
		},
		"more pairs than asked for": func(r *etcdserverpb.TxnRequest) *etcdserverpb.TxnResponse {
			resp := honest(r, nil)

			var kvs []*mvccpb.KeyValue
			for i := range 50 {
				kvs = append(kvs, &mvccpb.KeyValue{Key: []byte("/p/svc/addr" + itoa(i)), Value: []byte(canary)})
			}

			resp.Responses[0] = rangeOp(kvs...)

			return resp
		},
	}

	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, _ := hostile(t, "/p", func(_ context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
				return answer(r), nil
			}, etcd.WithTimeout(2*time.Second))

			got := serverConfig{Addr: "default", Port: 1}

			err := p.Unmarshal([]string{"svc"}, &got)
			if err == nil {
				t.Fatal("a wrong answer was accepted")
			}

			noLeak(t, err, canary)

			// All or nothing: a server that can't be believed sets no field.
			if got.Addr != "default" || got.Port != 1 || got.Debug || got.Timeout != "" || got.TLS.CertFile != "" {
				t.Errorf("a wrong answer changed the struct: %+v", got)
			}
		})
	}
}

func TestValuesThatAreNotText(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"invalid UTF-8": "ok\xff\xfe" + canary,
		"NUL":           "ok\x00" + canary,
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, _ := provider(t, map[string]string{"/p/svc/addr": value, "/p/svc/port": "5"}, "/p")

			got := serverConfig{Addr: "default"}

			err := p.Unmarshal([]string{"svc"}, &got)

			fe := fieldError(err, "Addr")
			if fe == nil {
				t.Fatalf("a value that is not text was accepted: %v", err)
			}

			if fe.Key != "/p/svc/addr" {
				t.Errorf("Key = %q", fe.Key)
			}

			noLeak(t, err, canary, "\xff", "\x00")

			if got.Addr != "default" {
				t.Errorf("Addr = %q, a refused value must not be used", got.Addr)
			}

			if got.Port != 5 {
				t.Errorf("Port = %d: a problem with one field stops no other", got.Port)
			}
		})
	}
}

func TestAValueIsBounded(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{
		"/p/svc/addr": strings.Repeat("a", etcd.MaxValueBytes),
		"/p/svc/port": "5",
	}, "/p")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err != nil || len(got.Addr) != etcd.MaxValueBytes {
		t.Fatalf("a value of the limit: %d bytes, %v", len(got.Addr), err)
	}

	p, _ = provider(t, map[string]string{
		"/p/svc/addr": canary + strings.Repeat("a", etcd.MaxValueBytes),
		"/p/svc/port": "5",
	}, "/p")

	got = serverConfig{}

	err := p.Unmarshal([]string{"svc"}, &got)
	if fieldError(err, "Addr") == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("a value over the limit was accepted: %v", err)
	}

	noLeak(t, err, canary)

	if got.Addr != "" || got.Port != 5 {
		t.Errorf("got %+v", got)
	}
}

// All the values together are bounded too, not only each one, however many fields
// a struct has.
func TestTheWholeAnswerIsBounded(t *testing.T) {
	t.Parallel()

	each := etcd.MaxValueBytes
	// Over the total, and under what the client is allowed to receive, so that
	// only the total can refuse it.
	n := etcd.MaxTotalBytes/each + 1

	values := map[string]string{}
	for i := range n {
		values["/p/svc/f"+itoa(i)] = strings.Repeat("a", each)
	}

	p, _ := provider(t, values, "/p")

	dst := manyFields(n)

	err := p.Unmarshal([]string{"svc"}, dst)
	if err == nil || err.Error() != "etcd: could not read configuration: answer too large" {
		t.Fatalf("an answer of %d bytes: %v", n*each, err)
	}
}

// What the client buffers is bounded by the provider, whatever the client was
// made with: an answer much larger than any honest one is refused as it arrives,
// before it is read, so it is the limit that refuses it and not what is in it.
func TestAnAnswerBiggerThanAnyHonestOneIsNotBuffered(t *testing.T) {
	t.Parallel()

	p, _ := hostile(t, "/p", func(_ context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
		resp := honest(r, nil)

		// More than was asked for, and 2 MiB of it (under the 4 MiB the gRPC client allows unless told otherwise): read, this would be "not what
		// was asked for".
		resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{
			ResponseRange: &etcdserverpb.RangeResponse{Kvs: []*mvccpb.KeyValue{{Key: []byte("/x"), Value: make([]byte, 2<<20)}}},
		}})

		return resp, nil
	}, etcd.WithTimeout(10*time.Second))

	var got serverConfig

	err := p.Unmarshal([]string{"svc"}, &got)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v, want the size to be what refused it", err)
	}
}

func TestBadValuesAreReasonsNotValues(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, map[string]string{
		"/p/svc/port":   canary,
		"/p/svc/debug":  canary,
		"/p/svc/limits": "k:" + canary,
	}, "/p")

	var got serverConfig

	err := p.Unmarshal([]string{"svc"}, &got)
	if err == nil {
		t.Fatal("no error")
	}

	noLeak(t, err, canary)

	for _, field := range []string{"Port", "Debug", "Limits"} {
		if fieldError(err, field) == nil {
			t.Errorf("no problem for %s: %v", field, err)
		}
	}
}

func TestAnIntegerOutOfRangeKeepsItsCause(t *testing.T) {
	t.Parallel()

	type small struct {
		N int8 `config:"n"`
	}

	p, _ := provider(t, map[string]string{"/p/svc/n": "999"}, "/p")

	var got small

	err := p.Unmarshal([]string{"svc"}, &got)
	if fe := fieldError(err, "N"); fe == nil || !strings.Contains(fe.Err.Error(), "range") {
		t.Errorf("err = %v", err)
	}
}

func TestAClientThatWasNotMadeByTheLibraryIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	p := etcd.New(&clientv3.Client{}, "/p")

	var got serverConfig
	if err := p.Unmarshal([]string{"svc"}, &got); err == nil {
		t.Error("no error")
	}
}

func TestTooManyFieldsAreReportedByDescribeToo(t *testing.T) {
	t.Parallel()

	p, _ := provider(t, nil, "/p")

	if _, err := p.Describe([]string{"svc"}, manyFields(etcd.MaxFields+1)); err == nil {
		t.Error("Describe listed more fields than can be read")
	}
}
