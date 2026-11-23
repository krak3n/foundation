package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.krak3n.dev/foundation"
	"go.krak3n.dev/foundation/health/monitor"
)

// defaultProbeTimeout is how long a probe request is given.
const defaultProbeTimeout = 5 * time.Second

// NewProbe creates a health probe that checks the server's responsiveness
// by making an HTTP GET request to the injected probe handler.
//
// The server is healthy if it answers with a 2xx status. Anything else, or no
// answer within five seconds or before the context given to Status is done, is
// unhealthy. The request doesn't use a proxy, follow redirects or keep the
// connection open, and an https URL is verified as usual.
func NewProbe(f *foundation.F, url string) monitor.Probe {
	return newProbe(f, url, nil, defaultProbeTimeout)
}

// newProbe is [NewProbe] with a timeout, and optionally the certificate the
// server is known to be using. When pin is set the server must present exactly
// that certificate, whatever name it is for: the probe talks to its own server
// over the loopback address, which the certificate may not name, and this is
// stricter than checking a name.
func newProbe(f *foundation.F, url string, pin *x509.Certificate, timeout time.Duration) *probe {
	transport := &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout: timeout,
	}

	if pin != nil {
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,

			// The ordinary verification is replaced, not dropped: VerifyConnection
			// runs on every handshake and accepts only the pinned certificate.
			InsecureSkipVerify: true, //nolint:gosec
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || !cs.PeerCertificates[0].Equal(pin) {
					return errors.New("the server's certificate is not the one it was configured with")
				}

				return nil
			},
		}
	}

	return &probe{
		f:    f,
		mode: monitor.AllModes,
		url:  url,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,

			// The probe path answers for itself, so a redirect is not healthy.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type probe struct {
	url    string
	mode   monitor.Mode
	client *http.Client
	f      *foundation.F
}

func (p *probe) Name() string {
	return p.f.String()
}

func (p *probe) Mode() monitor.Mode {
	return p.mode
}

func (p *probe) Status(ctx context.Context) (monitor.Status, error) {
	p.f.Log().Debug("checking server health", slog.String("url", p.url))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return monitor.StatusUnhealthy, errors.New("the probe request is invalid")
	}

	rsp, err := p.client.Do(req)
	if err != nil {
		// The error repeats the address, which whoever reads the health endpoint
		// doesn't need; the cause is what matters.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return monitor.StatusUnhealthy, err
	}

	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(rsp.Body, 4096))

		if err := rsp.Body.Close(); err != nil {
			p.f.Log().Error("failed to close probe response body", slog.String("err", err.Error()))
		}
	}()

	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		return monitor.StatusUnhealthy, fmt.Errorf("unexpected status %d", rsp.StatusCode)
	}

	return monitor.StatusHealthy, nil
}

// probeURL is where the probe at path reaches a server listening on addr. A
// listener on the wildcard address, as ":8080" is, is probed on loopback: the
// wildcard isn't somewhere to connect to, and where it goes varies.
func probeURL(addr net.Addr, useTLS bool, path string) string {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}

	var host, port string

	if tcp, ok := addr.(*net.TCPAddr); ok {
		host, port = loopbackIfWildcard(tcp.IP), strconv.Itoa(tcp.Port)
	} else if h, p, err := net.SplitHostPort(addr.String()); err == nil {
		host, port = h, p

		// A name is used as it is; only the wildcard, and nothing at all, are replaced.
		if ip := net.ParseIP(h); ip != nil || h == "" {
			host = loopbackIfWildcard(ip)
		}
	} else {
		return scheme + "://" + addr.String() + path
	}

	return scheme + "://" + net.JoinHostPort(host, port) + path
}

// loopbackIfWildcard returns ip as text, or the loopback address of the same
// family if it is the wildcard or there is none.
func loopbackIfWildcard(ip net.IP) string {
	switch {
	case len(ip) == 0:
		return net.IPv4(127, 0, 0, 1).String()
	case ip.IsUnspecified():
		if ip.To4() == nil {
			return net.IPv6loopback.String()
		}

		return net.IPv4(127, 0, 0, 1).String()
	default:
		return ip.String()
	}
}
