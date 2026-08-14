package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/scenario"
)

// Namespace and lane constants.
const (
	// DefaultNamespace is the state lane a request with no /n/ prefix and no
	// namespace header is served in. It is a real namespace, not a null one: the
	// single-test case is simply the case where every request shares one lane.
	DefaultNamespace = "default"

	// NamespaceHeader selects a namespace for SDKs that pin the request path and
	// cannot carry the /n/ prefix. The path prefix is the documented mechanism and
	// wins when both are present, because a base URL needs no consumer code change
	// at all while a per-request header often cannot be set.
	NamespaceHeader = "X-Servicesim-Namespace"

	// MaxNamespaceNameLen bounds a namespace or scenario name taken from a request.
	// Both become map keys and both are echoed nowhere, so the bound is about
	// cardinality and log volume rather than buffer safety.
	MaxNamespaceNameLen = 64
)

// Finding codes lane resolution raises.
const (
	// CodeNamespaceInvalid is raised when a /n/ prefix or namespace header carries
	// a name that is not a short safe identifier. The request is answered with a
	// provider-shaped error and served in the default namespace, so a rejected
	// request never creates state under a name nothing validated.
	CodeNamespaceInvalid = "namespace.invalid"

	// CodeScenarioInvalid is raised when an /x/ prefix carries a name that is not a
	// short safe identifier. Whether a well-formed name names a scenario that
	// exists is a separate question, answered by scenario selection.
	CodeScenarioInvalid = "scenario.invalid_name"

	// CodeTurnKeyUnresolved is raised when a turn_key extractor resolves nothing on
	// this request. The lane is then named by the route and the extractors that did
	// resolve, and the warning is mandatory: silently merging two lanes is the exact
	// failure the turn key exists to prevent, so it has to be visible in the journal
	// rather than inferred later from a response that came from the wrong lane.
	CodeTurnKeyUnresolved = "scenario.turn_key_unresolved"
)

// Prefix segments and the separators that join a lane key. Namespace and scenario
// names are validated to exclude both separators, so a key composed from them
// cannot be re-split into a different (namespace, key) pair.
const (
	scenarioSegment  = "x"
	namespaceSegment = "n"

	// namespaceSeparator joins the namespace to the rest of the lane key.
	namespaceSeparator = "/"

	// laneKeySeparator joins one turn_key extractor's contribution to the next.
	laneKeySeparator = "|"

	// maxLaneValueLen bounds one extractor's contribution. A body field is
	// client-supplied and unbounded; an unbounded value would become an unbounded
	// map key held for the life of the process. An over-long value is reported
	// unresolved rather than truncated, because truncation merges lanes silently
	// and that is the one outcome this whole mechanism exists to prevent.
	maxLaneValueLen = 128
)

// Lane identifies an isolated state lane. Turn cursors, fault attempt counters
// and journal scoping are all keyed on it, so they cannot disagree about which
// call this is.
//
// The three fields are three different dimensions and are deliberately not
// collapsed into one string on the way in:
//
//   - Namespace is the state boundary. Two tests sharing one container and one
//     scenario need independent cursors; that is the common case and a
//     scenario-only mechanism cannot express it.
//   - Scenario is the behaviour dimension, selected by the /x/ prefix and
//     defaulting to the loaded scenario's name.
//   - Key is the route this request matched, plus whatever additional
//     discriminators the provider's turn_key extractors resolved. It is what keeps
//     N concurrent agent roles on one LLM route from drawing turns out of one
//     another's sequence.
type Lane struct {
	// Namespace is DefaultNamespace when the request carries no /n/ prefix.
	Namespace string

	// Scenario is the loaded scenario's name when the request carries no /x/
	// prefix.
	Scenario string

	// Key opens with Route.FaultKey and continues with one component per turn_key
	// extractor that resolved something. The route is always present: turn_key
	// subdivides a route's lane and never replaces it.
	Key string
}

// CursorKey returns the single string every per-lane counter is keyed on: turn
// selection, fault attempt counting and anything else that must agree about which
// call this is.
//
// The route is always in it, because it is always in Key. A cursor key with no
// route component would be one two different routes could share, and the fault
// engine — which registers one plan per Route.FaultKey and recovers it out of the
// lane key — could resolve no plan for it, so no declared fault would fire and
// CallIndex would stay -1 for the life of the process.
//
// The default namespace contributes nothing, so an unprefixed request keys on
// exactly the string the pre-namespace code used — which is what lets a fault
// engine that pre-registered one counter per Route.FaultKey keep serving
// unprefixed traffic unchanged.
//
// Scenario is deliberately absent. The design's keying rule names the namespace
// as the outermost component and nothing else: a namespace is the state boundary,
// and a test that wants independent state asks for a namespace rather than
// relying on a behaviour selector to imply one.
func (l Lane) CursorKey() string {
	if l.Namespace == "" || l.Namespace == DefaultNamespace {
		return l.Key
	}
	return l.Namespace + namespaceSeparator + l.Key
}

// SplitCursorKey decomposes a key produced by [Lane.CursorKey] back into the
// namespace it was scoped to and the extractor contributions that named the lane.
//
// It exists for the fault engine, which pre-registers one plan per
// Route.FaultKey and is then asked for a lane key that embeds it. Recovering the
// route key by splitting is what lets a namespaced request draw on the plan its
// route declares while counting on a namespaced counter of its own — the
// isolation rule the design states as "fault attempt counters are isolated per
// namespace, the loaded scenario is not".
//
// It lives here, beside CursorKey, deliberately. A second copy of the separator
// grammar in internal/faults would be a second place to get it wrong, and the
// failure mode of the two disagreeing is a namespace that silently shares another
// namespace's attempt budget.
//
// The split is unambiguous because both separators are excluded from every
// component: ValidNamespace rejects them in a namespace name, and an extractor
// contribution is "<extractor>=<value>" where the extractor names are fixed. A
// key with no namespace prefix reports DefaultNamespace, so the caller never has
// to special-case unprefixed traffic.
//
// Every key [Lane.CursorKey] composes opens with a Route.FaultKey, so the parts
// returned for one always contain the route key the caller is looking for. A
// hand-built key need not, which is why the caller still checks.
func SplitCursorKey(key string) (namespace string, parts []string) {
	namespace = DefaultNamespace
	if ns, rest, ok := strings.Cut(key, namespaceSeparator); ok && ValidNamespace(ns) {
		namespace, key = ns, rest
	}
	if key == "" {
		return namespace, nil
	}
	return namespace, strings.Split(key, laneKeySeparator)
}

// ValidNamespace reports whether name is a short safe identifier: one to
// MaxNamespaceNameLen characters of ASCII letters, digits, '-' and '_'.
//
// The rule is restrictive on purpose. An unvalidated name arrives from the
// request path, becomes a map key with unbounded cardinality, is written to
// structured logs, and is one string concatenation away from being treated as a
// path. Rejecting '.', '/', '%' and every control character closes traversal and
// log injection at the same point, and the names this is for — a test ID — never
// need them.
func ValidNamespace(name string) bool {
	if name == "" || len(name) > MaxNamespaceNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// lanePrefix is what the optional path prefixes carried. It is produced once, by
// the mux, before route matching, and read back by Handle out of the request
// context — never re-derived, so the route the mux matched and the lane the
// handler serves cannot disagree.
type lanePrefix struct {
	// scenario and namespace are the validated values, empty when absent.
	scenario  string
	namespace string

	// scenarioBad and namespaceBad record that a prefix was present and rejected.
	// The offending value is deliberately not retained: it is attacker-controlled
	// text and the finding must not echo it into a log.
	scenarioBad  bool
	namespaceBad bool

	// path is the request path as received, before stripping. The journal records
	// what the client sent, not what routing rewrote it to.
	path string
}

// lanePrefixKey is the context key the mux hands the parsed prefix to Handle
// under. It is an unexported empty struct so no other package can collide with
// it or read it.
type lanePrefixKey struct{}

// withLanePrefix returns ctx carrying p.
func withLanePrefix(ctx context.Context, p lanePrefix) context.Context {
	return context.WithValue(ctx, lanePrefixKey{}, p)
}

// lanePrefixFrom returns the prefix the mux parsed, and whether there was one.
func lanePrefixFrom(ctx context.Context) (lanePrefix, bool) {
	p, ok := ctx.Value(lanePrefixKey{}).(lanePrefix)
	return p, ok
}

// requestLanePrefix returns the prefix for r: the one the mux parsed if this
// request came through NewMux, and otherwise one parsed from the path here.
//
// The fallback is what keeps Handle correct when it is used directly, without the
// mux — a supported way to build a listener and the shape most of this package's
// own tests use. Both paths run the same parser, so they cannot drift.
func requestLanePrefix(r *http.Request) lanePrefix {
	if p, ok := lanePrefixFrom(r.Context()); ok {
		return p
	}
	_, p := splitLanePath(r.URL.Path)
	return p
}

// splitLanePath strips the two optional path prefixes and returns the route path
// that remains together with what they carried.
//
//	/x/<scenario>/n/<namespace>/search  ->  /search
//	/n/<namespace>/search               ->  /search
//	/search                             ->  /search
//
// Both prefixes are optional and their order is fixed, x before n. Stripping
// happens before route matching, which is the property that makes base-URL
// injection work: a prefixed request matches the same ServeMux pattern as an
// unprefixed one, so no provider package learns that namespaces exist.
//
// A prefix whose value fails validation is still stripped. Rejecting it here
// would answer with net/http's plain-text 404 instead of the provider's own error
// envelope; stripping and recording the rejection routes the request to the
// handler that knows how to shape the error, which then fails it.
func splitLanePath(path string) (route string, p lanePrefix) {
	p.path = path
	rest := path

	if value, tail, ok := cutLaneSegment(rest, scenarioSegment); ok {
		rest = tail
		if ValidNamespace(value) {
			p.scenario = value
		} else {
			p.scenarioBad = true
		}
	}
	if value, tail, ok := cutLaneSegment(rest, namespaceSegment); ok {
		rest = tail
		if ValidNamespace(value) {
			p.namespace = value
		} else {
			p.namespaceBad = true
		}
	}

	if rest == "" {
		// "/n/<namespace>" with no route left addresses the listener root, which is
		// the catch-all's job to refuse. Returning "" would match no pattern at all.
		rest = "/"
	}
	return rest, p
}

// cutLaneSegment removes a leading "/<segment>/<value>" from path, returning the
// value and the remainder. An empty value ("/n//search") is reported as a present
// segment with an empty value, which fails validation — a present-but-blank
// namespace is an authoring mistake and must not be read as "no namespace".
func cutLaneSegment(path, segment string) (value, rest string, ok bool) {
	prefix := "/" + segment + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", path, false
	}
	rest = path[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i:], true
	}
	return rest, "", true
}

// Names of the two selectors that can carry a namespace, used to report which
// one a rejected name arrived on. A finding that named the wrong mechanism would
// send the reader looking at a base URL when the fault is in a header.
const (
	namespaceSourcePath   = "path"
	namespaceSourceHeader = "header"
)

// requestNamespace resolves the namespace a request is served in, and reports
// which selector carried a name that was rejected.
//
// It reads only the prefix the mux already parsed and one header — never the
// body — so it can run before the body read, which is what lets [Handle] claim
// the journal sequence number from the lane that will retain the entry. Claiming
// it later, or from the default lane, would leave an entry stored in one
// namespace carrying a Seq drawn from another's counter, which is precisely the
// disagreement [Lane] exists to prevent.
//
// A rejected name is served in the default namespace, so nothing ever allocates
// state under a name that failed validation.
func requestNamespace(r *http.Request, p lanePrefix) (namespace, rejected string) {
	switch {
	case p.namespaceBad:
		return DefaultNamespace, namespaceSourcePath
	case p.namespace != "":
		return p.namespace, ""
	}

	// The header is the fallback for SDKs that pin the path. The path prefix wins
	// when both are present, which is why this is only reached when no /n/ prefix
	// was supplied.
	switch header := r.Header.Get(NamespaceHeader); {
	case header == "":
		return DefaultNamespace, ""
	case ValidNamespace(header):
		return header, ""
	default:
		return DefaultNamespace, namespaceSourceHeader
	}
}

// resolveLane resolves the request's lane exactly once and stores it on the
// exchange, before fault selection and before turn selection so that the two draw
// on one counter rather than deriving a key each.
//
// It runs after the body has been read, because a body_json extractor needs it,
// and before the handler runs, because the handler may claim an attempt.
//
// The namespace half was already settled by [requestNamespace], which [Handle]
// called to claim the sequence number. This recomputes it from the same two
// inputs rather than being handed the answer, because the function is pure and
// the alternative — threading it through — is a second field that can disagree
// with the one on the exchange. What this adds is the finding: a rejection is
// reported here, once, where the exchange exists to carry it.
func resolveLane(x *Exchange, p lanePrefix) {
	namespace, rejected := requestNamespace(x.Request, p)
	switch rejected {
	case namespaceSourcePath:
		x.Fail(CodeNamespaceInvalid, "namespace",
			"the /n/ path prefix must name a namespace of 1 to %d characters from [A-Za-z0-9_-]",
			MaxNamespaceNameLen)
	case namespaceSourceHeader:
		x.Fail(CodeNamespaceInvalid, "namespace",
			"%s must carry a namespace of 1 to %d characters from [A-Za-z0-9_-]",
			NamespaceHeader, MaxNamespaceNameLen)
	}

	name := x.Deps.Scenario.Name
	switch {
	case p.scenarioBad:
		x.Fail(CodeScenarioInvalid, "scenario",
			"the /x/ path prefix must name a scenario of 1 to %d characters from [A-Za-z0-9_-]",
			MaxNamespaceNameLen)
	case p.scenario != "":
		name = p.scenario
	}

	x.lane = Lane{Namespace: namespace, Scenario: name, Key: turnLaneKey(x)}

	// Keep the unclaimed decision's key in step with the lane, so a request that
	// never claims an attempt still journals the budget it would have drawn on.
	x.decision.Key = x.lane.CursorKey()
}

// turnLaneKey composes the lane key: the route the request matched, followed by
// whatever the provider entry's remaining turn_key extractors resolved, in
// declaration order.
//
// The route is not optional and turn_key cannot remove it. turn_key declares
// discriminators ADDITIONAL to the route — one lane per model, per role, per
// tenant — never a replacement for it. Two routes sharing one cursor is a lane
// nobody wants, and a lane with no route component is worse than useless: the
// fault engine registers one plan per Route.FaultKey and recovers it by finding
// that key among the lane key's parts, so a routeless lane resolves no plan at
// all. Every fault the scenario declares stops firing, CallIndex stays -1 and
// `when: {call_index: 0}` becomes permanently unmatchable — silence where the
// author asked for behaviour.
//
// An explicit "route" therefore stays legal and is de-duplicated rather than
// doubled, so the documented default ["route"] and every scenario written against
// it produce byte-identical keys to before.
//
// The entry is the one named for this listener's provider. A listener serving a
// second scenario entry — Perplexity's Agent surface is one — keys its lanes on
// the primary entry's turn_key; per-entry keys would mean resolving the lane
// after the handler has chosen an entry, which is exactly the "derive it twice"
// shape the single-resolution rule forbids.
//
// An extractor that resolves nothing contributes nothing and raises
// CodeTurnKeyUnresolved, leaving the lane named by the route and the extractors
// that did resolve. The request is still served — loudly, in the journal, rather
// than silently in a lane the author did not intend.
func turnLaneKey(x *Exchange) string {
	extractors := entryTurnKey(x).Extractors()

	// The default is one lane per route, and it must produce Route.FaultKey
	// verbatim: that is the key a fault engine pre-registered at construction.
	// A turn_key of nothing but "route" entries says the same thing.
	if !hasDiscriminator(extractors) {
		return x.Route.FaultKey
	}

	var (
		decoded any
		didJSON bool
	)
	parts := make([]string, 1, len(extractors)+1)
	parts[0] = x.Route.FaultKey
	for _, extractor := range extractors {
		switch {
		case extractor == scenario.TurnKeyRoute:
			// Already the leading component. Doubling it would name a second lane
			// for what the author wrote as one.

		case strings.HasPrefix(extractor, scenario.TurnKeyBodyJSONPrefix):
			if !didJSON {
				decoded, didJSON = decodeLaneBody(x.Raw), true
			}
			path := strings.TrimPrefix(extractor, scenario.TurnKeyBodyJSONPrefix)
			value, ok := lookupLaneJSON(decoded, path)
			parts = appendLanePart(x, parts, extractor, value, ok)

		case strings.HasPrefix(extractor, scenario.TurnKeyHeaderPrefix):
			name := strings.TrimPrefix(extractor, scenario.TurnKeyHeaderPrefix)
			value := x.Request.Header.Get(name)
			parts = appendLanePart(x, parts, extractor, value, value != "")

		default:
			// scenario.Validate rejects an unknown extractor at load, so this is
			// reachable only through a hand-built entry. Warn rather than panic.
			x.Warn(CodeTurnKeyUnresolved, "turn_key",
				"turn_key extractor %q is not a recognised form", extractor)
		}
	}

	return strings.Join(parts, laneKeySeparator)
}

// hasDiscriminator reports whether the extractor list asks for anything beyond
// the route. It is what lets the default — and a turn_key that only restates the
// implicit "route" — take the allocation-free path and produce Route.FaultKey
// verbatim.
func hasDiscriminator(extractors []string) bool {
	for _, extractor := range extractors {
		if extractor != scenario.TurnKeyRoute {
			return true
		}
	}
	return false
}

// appendLanePart adds one extractor's contribution, or warns and adds nothing.
// The extractor name is part of the segment, so two extractors that resolve to
// the same text still name different lanes — without it, ["header:a","header:b"]
// would put a="x" and b="x" in one lane.
func appendLanePart(x *Exchange, parts []string, extractor, value string, ok bool) []string {
	switch {
	case !ok:
		x.Warn(CodeTurnKeyUnresolved, "turn_key",
			"turn_key extractor %q resolved nothing on this request; the lane is named by the route and the extractors that did",
			extractor)
		return parts
	case len(value) > maxLaneValueLen:
		x.Warn(CodeTurnKeyUnresolved, "turn_key",
			"turn_key extractor %q resolved a value longer than %d bytes and was not used",
			extractor, maxLaneValueLen)
		return parts
	}
	return append(parts, extractor+"="+value)
}

// entryTurnKey returns the turn key declared for this listener's provider entry,
// tolerating a scenario that declares no such entry.
func entryTurnKey(x *Exchange) scenario.TurnKey {
	e := x.Entry()
	if e == nil {
		return nil
	}
	return e.TurnKey
}

// decodeLaneBody decodes the raw body for extractor lookups. It decodes with
// UseNumber, matching scenario.Match exactly: a lane extractor and a
// `when: {body_json: ...}` predicate reading the same path must agree on the
// string, or a turn would be selected for a lane it was not written for.
func decodeLaneBody(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

// lookupLaneJSON walks a dotted path over a decoded JSON value, treating a
// numeric segment as an array index, and formats the scalar it lands on. A path
// that lands on an object or an array resolves nothing: a lane named by a
// serialised subtree is a lane key with no bound.
func lookupLaneJSON(v any, path string) (string, bool) {
	if v == nil || path == "" {
		return "", false
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return "", false
			}
			cur = node[i]
		default:
			return "", false
		}
	}
	return formatLaneScalar(cur)
}

// formatLaneScalar renders a decoded JSON scalar the way scenario.Match does.
// A JSON null resolves to nothing rather than to the string "null": a lane named
// by an absent value and a lane named by an explicit null are the same lane to
// every consumer, and treating null as resolved would hide the warning that says
// so.
func formatLaneScalar(v any) (string, bool) {
	switch value := v.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case json.Number:
		return value.String(), true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	default:
		return "", false
	}
}
