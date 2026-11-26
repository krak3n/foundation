package etcdtest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// A Call is a request the server received: the gRPC method, such as
// "/etcdserverpb.KV/Range", and the request message (an
// *etcdserverpb.RangeRequest, *etcdserverpb.TxnRequest and so on).
type Call struct {
	Method  string
	Request any
}

// An Option changes a [Server].
type Option interface {
	apply(*Server)
}

// WithKV makes the server answer the key value calls with kv instead of its own
// keys, so that a test can send what no honest server would: an answer for a key
// that was not asked for, an enormous value, an error that names a secret. The
// calls are still recorded. Put, Delete and Revision of the server are of no use
// then, as it holds nothing the answers come from.
func WithKV(kv etcdserverpb.KVServer) Option {
	return optionFunc(func(s *Server) { s.kv = kv })
}

// A Server is a fake etcd. It is safe for concurrent use.
type Server struct {
	lis  net.Listener
	srv  *grpc.Server
	cert tls.Certificate
	pool *x509.CertPool

	kv etcdserverpb.KVServer

	mu    sync.Mutex
	store *store
	calls []Call
}

// NewServer starts a server on a loopback port and stops it when the test ends.
func NewServer(tb testing.TB, opts ...Option) *Server {
	tb.Helper()

	cert, pool, err := newCertificate()
	if err != nil {
		tb.Fatalf("etcdtest: certificate: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("etcdtest: listen: %v", err)
	}

	s := &Server{lis: lis, cert: cert, pool: pool, store: newStore()}
	s.kv = s.store

	for _, opt := range opts {
		if opt != nil {
			opt.apply(s)
		}
	}

	s.srv = grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})),
		grpc.ChainUnaryInterceptor(s.record),
	)

	etcdserverpb.RegisterKVServer(s.srv, s.kv)

	go func() { _ = s.srv.Serve(lis) }()

	tb.Cleanup(s.srv.Stop)

	return s
}

// Close stops the server now, so that a test can see what a program does when it
// is gone. It is also done when the test ends, and doing it twice is fine.
func (s *Server) Close() { s.srv.Stop() }

// Endpoint is the address the server listens on, as host:port.
func (s *Server) Endpoint() string { return s.lis.Addr().String() }

// Client returns a client for the server that trusts only the certificate it made,
// and closes it when the test ends. The client is the real one: what is tested is
// what a program would do.
func (s *Server) Client(tb testing.TB) *clientv3.Client {
	tb.Helper()

	c, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{s.Endpoint()},
		DialTimeout: 5 * time.Second,
		TLS:         s.TLSConfig(),
		Logger:      zap.NewNop(),
	})
	if err != nil {
		tb.Fatalf("etcdtest: client: %v", err)
	}

	tb.Cleanup(func() { _ = c.Close() })

	return c
}

// TLSConfig is the client side TLS settings that trust this server's certificate
// and no other, for a test that builds its own client.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS13}
}

// Put sets key to value.
func (s *Server) Put(key, value string) { s.store.put(key, []byte(value)) }

// Delete removes key, if it is there.
func (s *Server) Delete(key string) { s.store.delete(key) }

// Revision is the revision of the keys, which every Put and Delete that changes
// something raises by one.
func (s *Server) Revision() int64 { return s.store.revision() }

// Calls returns the requests received so far, oldest first.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Call(nil), s.calls...)
}

// record keeps every call, before it is answered.
func (s *Server) record(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: info.FullMethod, Request: req})
	s.mu.Unlock()

	return next(ctx, req)
}

type optionFunc func(*Server)

func (f optionFunc) apply(s *Server) { f(s) }

// newCertificate makes a certificate for 127.0.0.1 that is its own authority, and a
// pool that trusts it and nothing else.
func newCertificate() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "etcdtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool, nil
}

// A store is the keys of the server. A Range may read a key, a range or everything
// from a key on, as etcd does.
type store struct {
	etcdserverpb.UnimplementedKVServer

	mu   sync.Mutex
	rev  int64
	data map[string][]byte
	mod  map[string]int64
}

func newStore() *store {
	return &store{rev: 1, data: map[string][]byte{}, mod: map[string]int64{}}
}

// Range answers a read.
func (st *store) Range(_ context.Context, req *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	return st.rangeLocked(req), nil
}

// Put sets a key.
func (st *store) Put(_ context.Context, req *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.putLocked(string(req.Key), req.Value)

	return &etcdserverpb.PutResponse{Header: st.header()}, nil
}

// DeleteRange removes keys.
func (st *store) DeleteRange(_ context.Context, req *etcdserverpb.DeleteRangeRequest) (*etcdserverpb.DeleteRangeResponse, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	n := st.deleteLocked(string(req.Key), string(req.RangeEnd))

	return &etcdserverpb.DeleteRangeResponse{Header: st.header(), Deleted: n}, nil
}

// Txn runs the operations of a transaction that has no comparisons: all of them
// are applied together, so a reader sees one revision. A comparison is not
// supported, and says so, rather than being treated as true.
func (st *store) Txn(_ context.Context, req *etcdserverpb.TxnRequest) (*etcdserverpb.TxnResponse, error) {
	if len(req.Compare) > 0 {
		return nil, status.Error(codes.Unimplemented, "etcdtest: comparisons are not supported")
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	resp := &etcdserverpb.TxnResponse{Succeeded: true}

	for _, op := range req.Success {
		switch r := op.Request.(type) {
		case *etcdserverpb.RequestOp_RequestRange:
			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseRange{ResponseRange: st.rangeLocked(r.RequestRange)}})
		case *etcdserverpb.RequestOp_RequestPut:
			st.putLocked(string(r.RequestPut.Key), r.RequestPut.Value)

			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponsePut{ResponsePut: &etcdserverpb.PutResponse{}}})
		case *etcdserverpb.RequestOp_RequestDeleteRange:
			n := st.deleteLocked(string(r.RequestDeleteRange.Key), string(r.RequestDeleteRange.RangeEnd))

			resp.Responses = append(resp.Responses, &etcdserverpb.ResponseOp{Response: &etcdserverpb.ResponseOp_ResponseDeleteRange{ResponseDeleteRange: &etcdserverpb.DeleteRangeResponse{Deleted: n}}})
		default:
			return nil, status.Error(codes.Unimplemented, "etcdtest: that operation is not supported")
		}
	}

	resp.Header = st.header()

	return resp, nil
}

func (st *store) put(key string, value []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.putLocked(key, value)
}

func (st *store) putLocked(key string, value []byte) {
	st.rev++
	st.data[key] = bytes.Clone(value)
	st.mod[key] = st.rev
}

func (st *store) delete(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.deleteLocked(key, "")
}

func (st *store) deleteLocked(key, end string) int64 {
	var n int64

	for _, k := range st.match(key, end) {
		delete(st.data, k)
		delete(st.mod, k)

		n++
	}

	if n > 0 {
		st.rev++
	}

	return n
}

func (st *store) revision() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()

	return st.rev
}

// match lists the keys the request names, in order.
func (st *store) match(key, end string) []string {
	var out []string

	for k := range st.data {
		switch {
		case end == "" && k == key:
			out = append(out, k)
		case end == "\x00" && k >= key:
			out = append(out, k)
		case end != "" && end != "\x00" && k >= key && k < end:
			out = append(out, k)
		}
	}

	sort.Strings(out)

	return out
}

func (st *store) header() *etcdserverpb.ResponseHeader {
	return &etcdserverpb.ResponseHeader{Revision: st.rev}
}

func (st *store) rangeLocked(req *etcdserverpb.RangeRequest) *etcdserverpb.RangeResponse {
	keys := st.match(string(req.Key), string(req.RangeEnd))

	resp := &etcdserverpb.RangeResponse{Header: st.header(), Count: int64(len(keys))}

	if req.Limit > 0 && int64(len(keys)) > req.Limit {
		keys = keys[:req.Limit]
		resp.More = true
	}

	if req.CountOnly {
		return resp
	}

	for _, k := range keys {
		kv := &mvccpb.KeyValue{Key: []byte(k), ModRevision: st.mod[k], CreateRevision: st.mod[k], Version: 1}

		if !req.KeysOnly {
			kv.Value = bytes.Clone(st.data[k])
		}

		resp.Kvs = append(resp.Kvs, kv)
	}

	return resp
}
