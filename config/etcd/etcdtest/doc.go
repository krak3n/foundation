// Package etcdtest is a fake etcd server for the tests of programs that read
// their configuration from etcd, as [net/http/httptest] is for HTTP. It is for
// tests only: never import it from code that is not a test (a test of this module
// checks that nothing does).
//
// A [Server] listens on a loopback port with TLS, using a certificate it makes for
// itself and throws away, so a client that is built from [Server.Client] checks
// what a real one would and nothing is read from or written to disk. It keeps its
// keys in memory and answers the calls a configuration provider makes (range, put,
// delete, and a transaction of those); every call is recorded, and [WithKV]
// replaces the answers altogether, to test what a provider does with a server that
// lies.
package etcdtest
