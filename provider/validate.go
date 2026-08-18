package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/scenario"
)

// CodeProviderUnimplemented is the finding raised for a provider named in a
// scenario that this build has no handler for. It is a warning, never an error:
// a scenario file shared across repositories must not break the moment one
// consumer pins an older Servicesim.
const CodeProviderUnimplemented = "scenario.provider.unimplemented"

// CodeTurnRouteUnknown is the finding raised for a `when.route:` naming a route
// the provider kind does not serve. It is an ERROR, not a warning: a turn whose
// route name matches nothing never fires, so the scenario quietly serves some
// other turn than the one its author wrote. A simulator exists to remove exactly
// that class of doubt, so the typo has to stop the process rather than change
// which response a consumer's test sees.
const CodeTurnRouteUnknown = "scenario.turn.route_unknown"

// RouteLister is implemented by a Validator whose provider package can name the
// routes its kind serves, which is what makes a `when.route:` value checkable at
// load rather than at request time.
//
// It is a separate optional interface, type-asserted at the call site, rather
// than a second method on Validator. Validator is exported and has
// implementations outside this repository's control, so widening it would break
// them for a check they can opt into instead (house rule 7). The same
// optional-interface shape is used for NamespaceAdmitter.
//
// The route set must be the one that KIND serves, not the whole package's.
// Perplexity registers six routes across two entries, and `route: agent` written
// in the Sonar block is an authoring error precisely because that entry does not
// serve it.
type RouteLister interface {
	Routes() []Route
}

// Validator is implemented by a provider package that can decode and validate its
// own projection bodies. It is the seam scenario.Validate cannot cross: under the
// open provider registry a `respond:` body is an undecoded YAML node whose Go
// type only the provider package knows, and scenario is a level-1 package that
// must not import profiles/exa to find out.
//
// This is a real seam with several implementations — one per provider package —
// so it does not repeat the anti-pattern the design review rejected when it
// declined a single-implementation FaultExecutor interface.
type Validator interface {
	// ValidateProjections decodes every turn's projection body in e and reports
	// what it finds, addressed by the entry's YAML path
	// (providers.<name>.turns[i].respond). It is given the whole scenario because
	// a projection references the shared corpus: a `source: source-a` that names
	// no declared source is the most common authoring error and only the provider
	// package can see it.
	//
	// It must not mutate the scenario, and it must be safe to call more than once.
	ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding

	// ProjectionKeys returns the top-level keys this validator's own
	// decode struct accepts in a turn's `respond:` body, for a doc
	// cross-check that would otherwise hand-mirror this package's decode
	// struct a second time (scenarios/scenarios_test.go's
	// documentedProjectionKeys, derived from profiles.Reference()'s own
	// validators rather than a parallel literal — Phase 10 unit 8). It is
	// a REQUIRED method, not an optional side interface on the RouteLister
	// pattern: unlike Routes (which only a package with routes to name
	// can answer), every Validator decodes SOME struct and can always
	// name its keys, even if that is nil (a validator that decodes
	// nothing, such as a profile with no Validators of its own —
	// noopValidator, profile.go). Widening the interface outright rather
	// than adding a third optional side interface is an engineering call,
	// not an owner one: four in-tree implementations, zero external,
	// still pre-1.0 (framework-seam.md, "Decisions for the owner").
	ProjectionKeys() []string
}

// ValidateScenario asks every provider named in the scenario to decode and
// validate its own projections. internal/server calls this after
// scenario.Validate and before readiness reports true, so acceptance criterion 4
// — "startup scenarios are deterministic and validated before readiness
// succeeds" — still holds end to end.
//
// A provider named in the scenario with no registered handler yields a warning
// naming it, never an error.
//
// handlers is keyed on ProviderEntry.Kind, which defaults to the block's name, so
// a scenario declaring an "openai" and an "openai_fallback" against one
// implementation registers that implementation once.
//
// It also sets ProviderEntry.Implemented, because this is the composition point
// that owns the handler registry and scenario.Validate deliberately does not know
// which providers exist in this build.
//
// Findings come back in scenario declaration order, never Go map order: a
// readiness failure that reordered its own reasons between runs would be
// miserable to diff.
func ValidateScenario(s *scenario.Scenario, handlers map[string]Validator) []scenario.Finding {
	if s == nil {
		return nil
	}
	var findings []scenario.Finding
	for _, name := range s.Providers.Names() {
		e := s.Providers.Get(name)
		if e == nil {
			continue
		}
		kind := e.Kind
		if kind == "" {
			kind = e.Name
		}
		v, ok := handlers[kind]
		if !ok || v == nil {
			e.Implemented = false
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     CodeProviderUnimplemented,
				Path:     "providers." + name,
				Message: fmt.Sprintf("no handler is registered for provider kind %q; this build serves nothing for it",
					kind),
			})
			continue
		}
		e.Implemented = true
		if lister, ok := v.(RouteLister); ok {
			findings = append(findings, validateTurnRoutes(name, kind, e, lister.Routes())...)
		}
		findings = append(findings, v.ValidateProjections(s, e)...)
	}
	return findings
}

// validateTurnRoutes checks every `when.route:` in an entry against the routes
// its kind actually serves, so a misspelled or misplaced name fails at load.
//
// It reports the served names in the message. A bare "route X is unknown" leaves
// the author guessing at a vocabulary they cannot see from the scenario file —
// the names come from Go source in a provider package, not from anything in
// their repository.
func validateTurnRoutes(name, kind string, e *scenario.ProviderEntry, routes []Route) []scenario.Finding {
	if len(routes) == 0 {
		return nil
	}
	var findings []scenario.Finding
	for i := range e.Turns {
		when := e.Turns[i].When
		if when == nil || when.Route == "" {
			continue
		}
		if slices.ContainsFunc(routes, func(r Route) bool {
			return scenario.RouteMatches(when.Route, r.FaultKey)
		}) {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     CodeTurnRouteUnknown,
			Path:     fmt.Sprintf("providers.%s.turns[%d].when.route", name, i),
			Message: fmt.Sprintf("route %q matches no route served by provider kind %q; it serves %s",
				when.Route, kind, strings.Join(routeNames(routes), ", ")),
		})
	}
	return findings
}

// routeNames lists the bare route names a route set offers, deduplicated and in
// registration order. Registration order, not sorted and never map order,
// because a readiness failure whose reasons reshuffle between runs is miserable
// to diff — the same rule ValidateScenario follows for findings.
func routeNames(routes []Route) []string {
	names := make([]string, 0, len(routes))
	for _, r := range routes {
		n := scenario.RouteKeySuffix(r.FaultKey)
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}
