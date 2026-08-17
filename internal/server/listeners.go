package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/internal/ids"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
)

// SurfaceAdmin is the name of the admin listener, as [Server.Addr] takes it.
// The provider listeners are named by their provider.Name — "exa", "tavily",
// "perplexity" — so one lookup covers every surface.
const SurfaceAdmin = "admin"

// CodeScenarioUnknown is the finding recorded for a request whose /x/<scenario>
// prefix names a scenario this process does not serve, and for one that names
// none where there is no default to fall back to.
//
// It is an error and the request is answered with a provider-shaped 404. Serving
// the default instead would be the failure this whole feature exists to prevent:
// a consumer would receive coherent-looking behaviour from a scenario it did not
// ask for, and the test would fail somewhere else entirely, much later.
const CodeScenarioUnknown = "scenario.unknown"

// scenarioSegment is the path segment carrying the /x/<scenario> selector. It
// mirrors the grammar provider.NewMux strips before route matching; this package
// only reads it, and the authoritative strip still happens there.
const scenarioSegment = "x"

// namespaceSegment is the path segment carrying the /n/<namespace> selector.
// This package reads it for its log events only — namespace isolation itself is
// the journal's and the fault engine's, keyed on the lane the provider seam
// resolves.
const namespaceSegment = "n"

// unmatchedPattern is the route a refused request is journaled under. It is the
// catch-all's pattern because that is what the request effectively hit: no route
// of a served scenario matched it.
const unmatchedPattern = "/"

// redactedName stands in for a scenario or namespace name that failed
// validation. The offending text is never logged: it arrives from the request
// path, and a log line is the surface most likely to be shipped somewhere with
// weaker access control.
const redactedName = "<invalid>"

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
func (s *Server) newSurfaces(adminDeps admin.Deps) []*surface {
	out := make([]*surface, 0, 1+len(s.cfg.Enabled()))
	out = append(out, s.newSurface(SurfaceAdmin, s.cfg.Admin, admin.Handler(adminDeps)))

	for _, name := range s.cfg.Enabled() {
		var listener config.Listener
		switch name {
		case provider.Exa:
			listener = s.cfg.Exa
		case provider.Tavily:
			listener = s.cfg.Tavily
		case provider.Perplexity:
			listener = s.cfg.Perplexity
		case provider.MCP:
			listener = s.cfg.MCP
		default:
			// Unreachable: config.Enabled only reports providers it has a
			// listener for. Skipping beats binding a port with no handler.
			continue
		}
		out = append(out, s.newSurface(string(name), listener, s.providerHandler(name)))
	}
	return out
}

// providerHandler builds one handler stack per loaded scenario and fronts them
// with the router that resolves which one a request asked for.
//
// One stack per scenario rather than one stack consulting a scenario per request
// is what keeps the provider packages ignorant that several scenarios exist: a
// handler is constructed with the Deps of exactly one scenario, exactly as it
// was before this feature, and selection happens in front of it.
func (s *Server) providerHandler(name provider.Name) http.Handler {
	r := &scenarioRouter{
		byName:      make(map[string]http.Handler, len(s.scenarios)),
		names:       s.ScenarioNames(),
		logger:      s.logger,
		unknown:     s.refusalHandler(name, "the /x/<scenario> prefix names a scenario this process does not serve"),
		undefaulted: s.refusalHandler(name, "this process serves no default scenario; select one with the /x/<scenario> path prefix"),
	}
	for _, ls := range s.scenarios {
		h := newProviderHandler(name, ls.deps)
		r.byName[ls.name] = h
		if ls == s.def {
			r.def = h
		}
	}
	return r
}

// newProviderHandler constructs one provider's listener handler from the Deps of
// one scenario.
//
// perplexity.New announces the Sonar sunset date once, here at construction,
// through deps.Logger. It is a property of the simulated API rather than of any
// request, so it belongs in the startup log and not in per-request noise. A
// process serving several scenarios announces it once per scenario, each line
// carrying that scenario's name, because each scenario is a distinct simulated
// API and one line would be silent about the rest.
func newProviderHandler(name provider.Name, deps provider.Deps) http.Handler {
	switch name {
	case provider.Exa:
		return exa.New(deps)
	case provider.Tavily:
		return tavily.New(deps)
	case provider.Perplexity:
		return perplexity.New(deps)
	case provider.MCP:
		return mcp.New(deps)
	default:
		// Unreachable: newSurfaces builds a surface only for the four names
		// above. A 404 beats a nil handler if that ever stops being true.
		return http.NotFoundHandler()
	}
}

// scenarioRouter dispatches a request to the handler stack of the scenario its
// /x/<scenario> prefix names, and fails closed when there is none.
//
// It is fixed at construction and never mutated, so it needs no lock: scenarios
// are loaded and validated at startup, and nothing pushes new behaviour into a
// running process (CLAUDE.md house rule 6).
type scenarioRouter struct {
	byName map[string]http.Handler

	// def serves a request carrying no /x/ prefix. It is nil when the
	// configuration supplies no default scenario, and such a request is then
	// refused rather than served by an arbitrary one.
	def http.Handler

	// unknown and undefaulted are the two fail-closed answers, kept apart so the
	// journal says which of the two happened.
	unknown     http.Handler
	undefaulted http.Handler

	// names is the loaded scenarios in registry order, for the refusal event.
	names []string

	logger *slog.Logger
}

// ServeHTTP resolves the scenario and hands the request on unchanged.
//
// The path is read, never rewritten: provider.NewMux strips the prefixes itself
// before route matching, and the journal records the path as the client sent it.
// Rewriting here would make the two disagree about what was requested.
func (r *scenarioRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	name, namespace := lanePrefixes(req.URL.Path)

	if name == "" {
		if r.def != nil {
			r.def.ServeHTTP(w, req)
			return
		}
		r.refuse(w, req, r.undefaulted, name, namespace)
		return
	}
	if h, ok := r.byName[name]; ok {
		h.ServeHTTP(w, req)
		return
	}
	r.refuse(w, req, r.unknown, name, namespace)
}

// refuse logs the refusal and serves the provider-shaped error.
//
// The event carries the namespace and the scenario, as every structured event on
// this path does, and carries a name only once it has passed validation:
// everything here arrives from the request path, and an unvalidated value must
// not reach a log line.
func (r *scenarioRouter) refuse(w http.ResponseWriter, req *http.Request, h http.Handler, name, namespace string) {
	r.logger.Warn("scenario.unknown",
		slog.String("scenario", logName(name)),
		slog.String("namespace", logNamespace(namespace)),
		slog.String("served", strings.Join(r.names, ",")))
	h.ServeHTTP(w, req)
}

// refusalHandler builds the fail-closed answer for one provider surface.
//
// It goes through provider.Handle rather than writing the response directly, so
// a refused request is journaled, logged and finding-carrying exactly like any
// other. "My test called /x/typo/search" is the question the journal has to be
// able to answer, and it cannot answer it about a request that never reached it.
//
// The Deps hold no scenario, which is the truth: none was selected. They do hold
// the process journal, so the entry lands in the caller's namespace alongside its
// other requests.
func (s *Server) refusalHandler(name provider.Name, reason string) http.Handler {
	deps := provider.Deps{
		Journal:             s.journal,
		Clock:               provider.SystemClock{},
		Logger:              s.logger,
		MaxRequestBytes:     s.cfg.MaxRequestBytes,
		MaxJournalBodyBytes: s.cfg.MaxJournalBodyBytes,
		MaxNamespaces:       s.cfg.MaxNamespaces,
	}
	body := scenarioNotFoundBody(name)
	served := strings.Join(s.ScenarioNames(), ", ")

	return provider.Handle(deps, name, provider.Route{Pattern: unmatchedPattern},
		func(x *provider.Exchange) provider.Response {
			x.Fail(CodeScenarioUnknown, "scenario", "%s; loaded scenarios: %s", reason, served)
			return provider.Response{
				Status: http.StatusNotFound,
				Body:   body,
				Label:  string(name) + "." + CodeScenarioUnknown,
			}
		})
}

// mcpCodeInvalidRequest is JSON-RPC's -32600 ("InvalidRequestError"), the
// code contracts/mcp/README.md's decision 6 assigns to this refusal.
const mcpCodeInvalidRequest = -32600

// mcpErrorEnvelope and mcpRPCError mirror the JSON-RPC error shape
// provider/mcp's own (unexported) errors.go builds, duplicated here rather
// than exported from that package solely for this one call site: a scenario
// refusal has no request to build a provider.Exchange for, so it cannot go
// through mcp.New's own handler.
type mcpErrorEnvelope struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Error   mcpRPCError `json:"error"`
}

// mcpRPCError is one JSON-RPC error object.
type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// scenarioNotFoundBody renders the body a refused request receives, in the shape
// that provider's own 404 uses.
//
// It is provider-shaped and carries no simulator detail, exactly like the
// routing 404 each provider package produces: a consumer decoding this response
// runs the same error path it runs against the real vendor. Why the request was
// refused is in the journal entry's findings and in the log, which is where a
// person looks and a consumer's error decoder does not.
func scenarioNotFoundBody(name provider.Name) []byte {
	switch name {
	case provider.Tavily:
		body, err := wire.Render(tavily.ErrorResponse{
			Detail: tavily.ErrorDetail{Error: tavily.MessageNotFound},
		}, nil)
		if err != nil {
			return []byte(`{"detail":{"error":"Not Found"}}`)
		}
		return body

	case provider.Perplexity:
		// Sonar's shape: the /x/ prefix is stripped before route matching, so a
		// refused request has not been resolved to the Sonar or the Agent surface
		// and the documented Sonar envelope is the one both routes share a client
		// for.
		return perplexity.ErrorBody(perplexity.SurfaceSonar, http.StatusNotFound, "")

	case provider.Exa:
		body, err := wire.Render(exa.ErrorResponse{
			// Derived, never random: the same refusal renders the same bytes on
			// every run, which is what determinism means here (CLAUDE.md house
			// rule 2). There is no scenario to seed it with, so the tuple is this
			// surface and this refusal.
			RequestID: ids.Hex32(string(name), CodeScenarioUnknown),
			Error:     http.StatusText(http.StatusNotFound),
			Tag:       exa.TagNotFound,
		}, nil)
		if err != nil {
			return []byte(`{"requestId":"","error":"Not Found","tag":"NOT_FOUND"}`)
		}
		return body

	case provider.MCP:
		// mcp's own JSON-RPC error envelope type is unexported (provider/mcp
		// builds it itself, per request, from the request's own id); this
		// refusal has no request to read an id from, so it renders the same
		// shape directly. No id (always null, the same as every other shape
		// failure this profile answers — contracts/mcp/README.md decision 6),
		// code -32600 InvalidRequestError, and the same generic message the
		// other three vendors' bodies carry here rather than naming the
		// scenario: the scenario name is in the journal and the log, which is
		// where a person looks, not in a response a consumer's error decoder
		// parses.
		body, err := wire.Render(mcpErrorEnvelope{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   mcpRPCError{Code: mcpCodeInvalidRequest, Message: http.StatusText(http.StatusNotFound)},
		}, nil)
		if err != nil {
			return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Not Found"}}`)
		}
		return body

	default:
		// Unreachable: newSurfaces builds a surface only for the four names
		// above. An empty body beats inventing a vendor's error shape.
		return nil
	}
}

// lanePrefixes reads the optional /x/<scenario> and /n/<namespace> path
// prefixes. Either is empty when absent.
//
// It reads the same fixed grammar provider.NewMux strips — x before n, both
// optional — and deliberately only reads it: the request is passed on untouched
// and the provider seam performs the authoritative strip, so routing and lane
// resolution cannot end up disagreeing about what the client asked for.
//
// A value that is not a valid selector is returned as it was found rather than
// discarded. The router refuses it either way, and the caller decides what may be
// logged.
func lanePrefixes(path string) (scenarioName, namespace string) {
	scenarioName, rest := cutLaneSegment(path, scenarioSegment)
	namespace, _ = cutLaneSegment(rest, namespaceSegment)
	return scenarioName, namespace
}

// cutLaneSegment removes a leading "/<segment>/<value>" from path, returning the
// value and the remainder. An empty value ("/n//search") is a present segment
// with an empty value, which is an authoring mistake and must not read as "no
// prefix"; it is returned as the empty string and refused downstream by the same
// validation every other malformed value meets.
func cutLaneSegment(path, segment string) (value, rest string) {
	prefix := "/" + segment + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", path
	}
	rest = path[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i:]
	}
	return rest, ""
}

// logName renders a scenario selector for a log line: the name when it is a
// valid selector, a fixed placeholder when it is not, and empty when the request
// carried none.
func logName(name string) string {
	switch {
	case name == "":
		return ""
	case provider.ValidNamespace(name):
		return name
	default:
		return redactedName
	}
}

// logNamespace renders a namespace for a log line. An absent namespace is the
// default one, because that is the lane the request is actually served in.
func logNamespace(namespace string) string {
	switch {
	case namespace == "":
		return provider.DefaultNamespace
	case provider.ValidNamespace(namespace):
		return namespace
	default:
		return redactedName
	}
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
