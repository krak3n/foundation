// Package etcd reads configuration from an etcd cluster, as the remote layer of a
// foundation program (config.RemoteLayer: above the documents, below the
// environment).
//
// A field is one key: the prefix the program gives, the words of the function's
// path in lower case, and the name of the field (its config tag), so a field
// addr of the function ["svc", "http.server"] under the prefix /services/shop is
// the key /services/shop/svc/http/server/addr, and a field of a nested struct adds
// its name to the path. Values are text, parsed as every provider parses them. A
// key that is not there leaves the field alone.
//
// Everything a function needs is read in one transaction, so it sees one revision,
// with a deadline, and nothing is written. The provider fails closed: a cluster
// that can't be reached, refuses, or answers wrongly is an error, never a
// fallback to what other layers hold. Errors give a reason, never a value, a key
// the cluster sent or an address.
//
// What the provider can't do is make the client safe. Build it with TLS, a limit
// on the size of what it receives and credentials of a role that may only read
// the prefix.
package etcd
