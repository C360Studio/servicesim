package provider

import (
	"fmt"

	"github.com/c360studio/servicesim/scenario"
)

// CodeProviderUnimplemented is the finding raised for a provider named in a
// scenario that this build has no handler for. It is a warning, never an error:
// a scenario file shared across repositories must not break the moment one
// consumer pins an older Servicesim.
const CodeProviderUnimplemented = "scenario.provider.unimplemented"

// Validator is implemented by a provider package that can decode and validate its
// own projection bodies. It is the seam scenario.Validate cannot cross: under the
// open provider registry a `respond:` body is an undecoded YAML node whose Go
// type only the provider package knows, and scenario is a level-1 package that
// must not import provider/exa to find out.
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
		findings = append(findings, v.ValidateProjections(s, e)...)
	}
	return findings
}
