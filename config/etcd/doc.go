// Package etcd reads configuration from an etcd cluster, as the remote layer of a
// foundation program (config.RemoteLayer: above the documents, below the environment).
//
// The provider is not written yet: this module is its place in the repository, released
// and tested on its own so that a client advisory can be fixed without a release of the
// core.
package etcd

// Nothing here uses the core yet, and go mod tidy drops a requirement that nothing
// imports, which the release cascade would then have nothing to move.
import _ "go.krak3n.dev/foundation/config"
