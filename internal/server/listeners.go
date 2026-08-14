package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
)

// SurfaceAdmin is the name of the admin listener, as [Server.Addr] takes it.
// The provider listeners are named by their provider.Name — "exa", "tavily",
// "perplexity" — so one lookup covers every surface.
const SurfaceAdmin = "admin"

// surface is one bound HTTP listener.
//
// The configured and the bound address are kept apart on purpose: with
// --admin-port 0 they differ, and the bound one is how a test that asked for an
// ephemeral port discovers the real one.
type surface struct {
	// name is what Server.Addr looks up: SurfaceAdmin or a provider.Name.
	name string

	// configured is the host:port from configuration, always including the
	// configured bind address, so a listener can never come up on an interface
	// the operator did not name.
	configured string

	// http is the server this surface serves. Its Handler is fixed at
	// construction; only the listener arrives later.
	http *http.Server

	// listener and bound are written by Server.bind and read under Server.mu.
	listener net.Listener
	bound    string
}

// newSurfaces builds every listener this configuration asks for, admin first.
//
// Order is load-bearing twice over. The admin listener binds first so that
// GET /healthz answers while the provider listeners are still binding, and it is
// shut down last, in reverse, so a container's health probe keeps getting an
// answer while the provider listeners drain. The provider order after it comes
// from config.Enabled, which is a fixed slice rather than a map range: a startup
// log that listed listeners in a different order on every run would be useless
// as evidence.
//
// A provider whose listener is disabled is not constructed at all, which is what
// lets the image serve a subset. Its scenario entry is left alone; it simply has
// nothing bound in front of it.
func (s *Server) newSurfaces(deps provider.Deps, adminDeps admin.Deps) []*surface {
	out := make([]*surface, 0, 1+len(s.cfg.Enabled()))
	out = append(out, s.newSurface(SurfaceAdmin, s.cfg.Admin, admin.Handler(adminDeps)))

	for _, name := range s.cfg.Enabled() {
		var (
			listener config.Listener
			handler  http.Handler
		)
		switch name {
		case provider.Exa:
			listener, handler = s.cfg.Exa, exa.New(deps)
		case provider.Tavily:
			listener, handler = s.cfg.Tavily, tavily.New(deps)
		case provider.Perplexity:
			// perplexity.New announces the Sonar sunset date once, here at
			// construction, through deps.Logger. It is a property of the
			// simulated API rather than of any request, so it belongs in the
			// startup log and not in per-request noise.
			listener, handler = s.cfg.Perplexity, perplexity.New(deps)
		default:
			// Unreachable: config.Enabled only reports providers it has a
			// listener for. Skipping beats binding a port with no handler.
			continue
		}
		out = append(out, s.newSurface(string(name), listener, handler))
	}
	return out
}

// newSurface wraps one handler in an http.Server carrying the configured
// timeouts.
//
// ReadHeaderTimeout is set from configuration and defended in depth: a zero
// value would leave a Slowloris client holding a connection open forever, and
// this process is expected to run unattended in CI.
//
// ErrorLog is routed into slog so that a connection-level error arrives in the
// same structured stream as everything else, rather than as an unstructured line
// on stderr that a JSON log consumer cannot parse.
func (s *Server) newSurface(name string, l config.Listener, h http.Handler) *surface {
	sf := &surface{
		name:       name,
		configured: s.cfg.Addr(l),
	}
	sf.http = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
		ConnState: func(c net.Conn, state http.ConnState) {
			if s.connState != nil {
				s.connState(name, c, state)
			}
		},
	}
	return sf
}

// bind opens the surface's listener on the configured interface and records the
// address that was actually bound.
//
// net.Listen is given host:port rather than ":port", so --bind-address 127.0.0.1
// binds loopback only and nothing on the machine's other interfaces can reach
// it. That is the default, and the image opts into 0.0.0.0 explicitly.
//
// A port already in use fails here, immediately and with the port in the
// message. It must never retry or wait: a simulator that hangs at startup fails
// a consumer's test suite as a timeout, which is the least diagnosable failure
// this process can produce.
func (s *Server) bind(sf *surface) error {
	ln, err := net.Listen("tcp", sf.configured)
	if err != nil {
		return fmt.Errorf("binding the %s listener on %s: %w", sf.name, sf.configured, err)
	}

	s.mu.Lock()
	sf.listener = ln
	sf.bound = ln.Addr().String()
	s.mu.Unlock()

	s.logger.Info("server.listening",
		slog.String("surface", sf.name),
		slog.String("address", ln.Addr().String()))
	return nil
}
