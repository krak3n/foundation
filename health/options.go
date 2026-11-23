package health

import (
	"time"

	"go.krak3n.dev/foundation/health/monitor"
)

// An Option configures the health handler and the health server alike, so it can
// be given to either: [WithVerbose], [WithProbeTimeout] and [WithResultsMarshaler].
type Option interface {
	ServerOption
	HandlerOption
}

// handlerOption is an option for the handler that can also be given to
// [Server], which passes it on.
type handlerOption func(*handlerConfig)

func (o handlerOption) applyHandlerConfig(c *handlerConfig) {
	o(c)
}

func (o handlerOption) applyServerConfig(c *ServerConfig) {
	c.HandlerOptions = append(c.HandlerOptions, o)
}

// WithVerbose allows a client to ask for the detail of each probe by adding
// ?verbose to the request: the name of each probe, whether it passed and, if it
// failed, why.
//
// It is off by default, and the response is then only the verdict. The names of
// probes and the text of their errors describe the service's internals, such as
// the hosts it talks to, and a health endpoint is usually reachable by anyone who
// can reach the port. When it is off the reason a probe fails is still logged,
// once each time it starts failing. The server can also be told with the
// VERBOSE configuration setting.
func WithVerbose() Option {
	return handlerOption(func(c *handlerConfig) {
		c.verbose = true
	})
}

// WithProbeTimeout sets how long each probe is given to answer, and how long a
// request waits for one. The default is 5 seconds; a value of zero or less means
// the default.
//
// A probe is passed a context with this deadline and should return when it
// expires. One that doesn't is reported as failed once the time is up, and is not
// started again until it returns.
func WithProbeTimeout(d time.Duration) Option {
	return handlerOption(func(c *handlerConfig) {
		c.probeTimeout = d
	})
}

// WithResultsMarshaler sets the marshaler for the health results shown by
// ?verbose. The default is a Kubernetes style marshaler, one line per probe.
// See https://kubernetes.io/docs/reference/using-api/health-checks/
func WithResultsMarshaler(m monitor.ResultsMarshaler) Option {
	return &withResultsMarshaler{m: m}
}

type withResultsMarshaler struct {
	m monitor.ResultsMarshaler
}

func (o *withResultsMarshaler) applyServerConfig(c *ServerConfig) {
	c.HandlerOptions = append(c.HandlerOptions, o)
}

func (o *withResultsMarshaler) applyHandlerConfig(c *handlerConfig) {
	c.marshaler = o.m
}
