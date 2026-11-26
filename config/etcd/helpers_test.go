package etcd_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"

	"go.krak3n.dev/foundation/config"
	"go.krak3n.dev/foundation/config/etcd"
	"go.krak3n.dev/foundation/config/etcd/etcdtest"
)

type tlsConfig struct {
	CertFile string `config:"cert_file"`
}

type serverConfig struct {
	Addr    string         `config:"addr"`
	Port    int            `config:"port"`
	Debug   bool           `config:"debug"`
	Timeout string         `config:"timeout"`
	Tags    []string       `config:"tags"`
	Limits  map[string]int `config:"limits"`
	TLS     tlsConfig      `config:"tls"`
}

// provider is a provider for a fresh fake server that holds values, with the keys
// written as they are (so a test spells out where a value lives).
func provider(t *testing.T, values map[string]string, prefix string, opts ...etcd.Option) (*etcd.Provider, *etcdtest.Server) {
	t.Helper()

	s := etcdtest.NewServer(t)

	for k, v := range values {
		s.Put(k, v)
	}

	return etcd.New(s.Client(t), prefix, opts...), s
}

// fieldErrors lists every *config.FieldError inside err, however deeply it is
// joined (errors.As finds only the first).
func fieldErrors(err error) []*config.FieldError {
	var out []*config.FieldError

	var walk func(error)

	walk = func(e error) {
		if e == nil {
			return
		}

		if fe, ok := e.(*config.FieldError); ok {
			out = append(out, fe)

			return
		}

		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range j.Unwrap() {
				walk(c)
			}

			return
		}

		if u := errors.Unwrap(e); u != nil {
			walk(u)
		}
	}

	walk(err)

	return out
}

// fieldError returns the problem about the named Go field, or nil.
func fieldError(err error, field string) *config.FieldError {
	for _, fe := range fieldErrors(err) {
		if fe.Field == field {
			return fe
		}
	}

	return nil
}

// noLeak fails if err repeats any of the texts: a value, a prefix, an address, a
// message of the server. Reasons only.
func noLeak(t *testing.T, err error, texts ...string) {
	t.Helper()

	if err == nil {
		return
	}

	for _, text := range texts {
		if text != "" && strings.Contains(err.Error(), text) {
			t.Errorf("the error repeats %q: %q", text, err.Error())
		}
	}
}

// txnCalls returns the transactions the server received.
func txnCalls(s *etcdtest.Server) []*etcdserverpb.TxnRequest {
	var out []*etcdserverpb.TxnRequest

	for _, c := range s.Calls() {
		if r, ok := c.Request.(*etcdserverpb.TxnRequest); ok {
			out = append(out, r)
		}
	}

	return out
}

// requestedKeys lists the keys a transaction reads, in order.
func requestedKeys(r *etcdserverpb.TxnRequest) []string {
	var keys []string

	for _, op := range r.Success {
		if rr := op.GetRequestRange(); rr != nil {
			keys = append(keys, string(rr.Key))
		}
	}

	return keys
}

// A hostileKV is a server whose transaction answers are written by the test.
type hostileKV struct {
	etcdserverpb.UnimplementedKVServer

	txn func(context.Context, *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error)
}

func (k hostileKV) Txn(ctx context.Context, r *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
	return k.txn(ctx, r)
}

// honest answers a transaction as a real server would for the given values.
func honest(r *etcdserverpb.TxnRequest, values map[string]string) *etcdserverpb.TxnResponse {
	resp := &etcdserverpb.TxnResponse{Header: &etcdserverpb.ResponseHeader{Revision: 7}, Succeeded: true}

	for _, op := range r.Success {
		rr := &etcdserverpb.RangeResponse{}

		if req := op.GetRequestRange(); req != nil {
			if v, ok := values[string(req.Key)]; ok {
				rr.Kvs = []*mvccpb.KeyValue{{Key: req.Key, Value: []byte(v)}}
				rr.Count = 1
			}
		}

		resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{ResponseRange: rr}})
	}

	return resp
}

// hostile is a provider for a server that answers transactions with fn.
func hostile(t *testing.T, prefix string, fn func(context.Context, *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error), opts ...etcd.Option) (*etcd.Provider, *etcdtest.Server) {
	t.Helper()

	s := etcdtest.NewServer(t, etcdtest.WithKV(hostileKV{txn: fn}))

	return etcd.New(s.Client(t), prefix, opts...), s
}

// manyFields makes a struct with n string fields named f0, f1, ...
func manyFields(n int) any {
	fields := make([]reflect.StructField, n)

	for i := range fields {
		name := "f" + itoa(i)
		fields[i] = reflect.StructField{
			Name: strings.ToUpper(name[:1]) + name[1:],
			Type: reflect.TypeFor[string](),
			Tag:  reflect.StructTag(`config:"` + name + `"`),
		}
	}

	return reflect.New(reflect.StructOf(fields)).Interface()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var b []byte

	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}

	return string(b)
}
