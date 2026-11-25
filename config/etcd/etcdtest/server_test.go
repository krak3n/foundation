package etcdtest_test

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.krak3n.dev/foundation/config/etcd/etcdtest"
)

func ctx(t *testing.T) context.Context {
	t.Helper()

	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	return c
}

func TestKeysAreKept(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	c := s.Client(t)

	s.Put("/a/b", "one")
	s.Put("/a/c", "two")
	s.Put("/z", "three")

	got, err := c.Get(ctx(t), "/a/b")
	if err != nil || len(got.Kvs) != 1 || string(got.Kvs[0].Value) != "one" {
		t.Fatalf("Get /a/b = %v, %v", got, err)
	}

	got, err = c.Get(ctx(t), "/a/", clientv3.WithPrefix())
	if err != nil || len(got.Kvs) != 2 {
		t.Fatalf("Get /a/ prefix = %v, %v", got, err)
	}

	got, err = c.Get(ctx(t), "/a/", clientv3.WithPrefix(), clientv3.WithLimit(1), clientv3.WithKeysOnly())
	if err != nil || len(got.Kvs) != 1 || len(got.Kvs[0].Value) != 0 || !got.More {
		t.Fatalf("limited keys-only Get = %v, %v", got, err)
	}

	s.Delete("/a/b")

	got, err = c.Get(ctx(t), "/a/b")
	if err != nil || len(got.Kvs) != 0 {
		t.Fatalf("Get after Delete = %v, %v", got, err)
	}
}

func TestRevisionRisesWithChanges(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)

	r0 := s.Revision()
	s.Put("/a", "1")
	s.Put("/a", "2")

	if r := s.Revision(); r != r0+2 {
		t.Errorf("Revision = %d after two puts, want %d", r, r0+2)
	}

	s.Delete("/missing")

	if r := s.Revision(); r != r0+2 {
		t.Errorf("Revision = %d after deleting nothing, want %d", r, r0+2)
	}
}

func TestTransactionReadsAtOneRevision(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	c := s.Client(t)

	s.Put("/a", "1")
	s.Put("/b", "2")

	resp, err := c.Txn(ctx(t)).Then(clientv3.OpGet("/a"), clientv3.OpGet("/b"), clientv3.OpGet("/none")).Commit()
	if err != nil {
		t.Fatal(err)
	}

	if !resp.Succeeded || len(resp.Responses) != 3 {
		t.Fatalf("Txn = %+v", resp)
	}

	if n := len(resp.Responses[2].GetResponseRange().Kvs); n != 0 {
		t.Errorf("a missing key answered with %d pairs", n)
	}
}

func TestComparisonsAreRefused(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	c := s.Client(t)

	// An answer that pretends the comparison held would let a test pass for
	// the wrong reason.
	_, err := c.Txn(ctx(t)).If(clientv3.Compare(clientv3.Value("/a"), "=", "1")).Then(clientv3.OpGet("/a")).Commit()
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("err = %v, want Unimplemented", err)
	}
}

func TestCallsAreRecorded(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)
	c := s.Client(t)

	if _, err := c.Get(ctx(t), "/a"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Txn(ctx(t)).Then(clientv3.OpGet("/a")).Commit(); err != nil {
		t.Fatal(err)
	}

	calls := s.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2", calls)
	}

	if calls[0].Method != "/etcdserverpb.KV/Range" {
		t.Errorf("first call = %q", calls[0].Method)
	}

	if _, ok := calls[1].Request.(*etcdserverpb.TxnRequest); !ok || calls[1].Method != "/etcdserverpb.KV/Txn" {
		t.Errorf("second call = %q %T", calls[1].Method, calls[1].Request)
	}

	// What is returned is a copy.
	_ = append(s.Calls(), etcdtest.Call{})

	if n := len(s.Calls()); n != 2 {
		t.Errorf("calls changed to %d by the caller", n)
	}
}

func TestPlainTextIsNotSpoken(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)

	c, err := clientv3.New(clientv3.Config{Endpoints: []string{"http://" + s.Endpoint()}, DialTimeout: time.Second})
	if err != nil {
		t.Skipf("no plain client: %v", err)
	}

	defer c.Close()

	short, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.Get(short, "/a"); err == nil {
		t.Error("a plain text client got an answer")
	}

	for _, call := range s.Calls() {
		t.Errorf("the server saw %q over plain text", call.Method)
	}
}

func TestEachServerHasACertificateOfItsOwn(t *testing.T) {
	t.Parallel()

	a, b := etcdtest.NewServer(t), etcdtest.NewServer(t)

	// A's client, pointed at B, must refuse B's certificate.
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{b.Endpoint()}, DialTimeout: time.Second, TLS: a.TLSConfig()})
	if err != nil {
		return // refused already at dial, which is the point
	}

	defer c.Close()

	short, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.Get(short, "/a"); err == nil {
		t.Error("a client of one server was answered by another")
	}

	if n := len(b.Calls()); n != 0 {
		t.Errorf("the other server answered %d calls", n)
	}
}

type liar struct {
	etcdserverpb.UnimplementedKVServer
}

func (liar) Range(context.Context, *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	return nil, errors.New("canary-from-a-lying-server")
}

func TestWithKVReplacesTheAnswers(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t, etcdtest.WithKV(liar{}))
	c := s.Client(t)

	s.Put("/a", "1") // goes to a store nothing reads

	_, err := c.Get(ctx(t), "/a")
	if err == nil || !strings.Contains(err.Error(), "canary-from-a-lying-server") {
		t.Errorf("err = %v, want the lying server's", err)
	}

	if n := len(s.Calls()); n != 1 {
		t.Errorf("calls = %d, want the one that was made recorded", n)
	}
}

func TestCloseStopsTheServer(t *testing.T) {
	t.Parallel()

	s := etcdtest.NewServer(t)

	conn, err := net.DialTimeout("tcp", s.Endpoint(), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	conn.Close()

	s.Close()
	s.Close() // twice is fine

	if conn, err := net.DialTimeout("tcp", s.Endpoint(), time.Second); err == nil {
		conn.Close()
		t.Error("the server accepts connections after Close")
	}
}

func TestServerStopsWithTheTest(t *testing.T) {
	t.Parallel()

	var endpoint string

	t.Run("inner", func(t *testing.T) {
		endpoint = etcdtest.NewServer(t).Endpoint()
	})

	if endpoint == "" {
		t.Fatal("no endpoint")
	}

	// The listener is closed when the subtest's cleanup has run.
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err == nil {
		conn.Close()
		t.Error("the server still accepts connections after its test ended")
	}
}

// Nothing but tests may import the fake: a program that did would carry a server
// that answers whatever it is told.
func TestNothingButTestsImportsIt(t *testing.T) {
	t.Parallel()

	const path = "go.krak3n.dev/foundation/config/etcd/etcdtest"

	root := filepath.Join("..")

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() && d.Name() == "etcdtest" {
			return filepath.SkipDir
		}

		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, "\"`") == path {
				t.Errorf("%s imports etcdtest", p)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
