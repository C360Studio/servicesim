package exa

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
)

// TestStreamWarnWithTruncateBodyStillLoads is the compatibility regression
// docs/design/streaming.md §9 requires: an already-shipped v1 fixture that
// combines `stream: warn` (declared since v0.1.0, on Exa's own
// scenario.StreamPolicy field, untouched by this design) with a
// truncate_body fault must keep loading with no findings after
// scenario.StreamServe and scenario.ValidateStreamFaultMismatch land.
//
// The distinction that makes this valid is declared POLICY versus produced
// OUTCOME, not the presence of a `stream:` key: `warn` produces an ordinary
// JSON body, so truncating its bytes is exactly as meaningful as it was
// before streaming existed anywhere in this repository. Only an entry whose
// EFFECTIVE policy is `stream` rejects truncate_body
// (scenario.CodeStreamFaultMismatch) — a policy Exa cannot even express yet,
// since its own Stream field stays scenario.StreamPolicy and never decodes
// the mapping form. newSim (handler_test.go) is what makes this an
// end-to-end proof rather than a unit test of one function in isolation: it
// runs the fixture through Exa's OWN, unmodified provider.Validator exactly
// as internal/server does before readiness.
func TestStreamWarnWithTruncateBodyStillLoads(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: stream-warn-truncate-regression
providers:
  exa:
    stream: warn
    fault:
      attempts:
        - kind: truncate_body
          truncate_after_bytes: 40
    results:
      - source: source-a
sources:
  - id: source-a
    url: https://example.test/a
    title: A
`

	// newSim only fails the test on an ERROR finding (see its doc comment),
	// which would let a WARNING this fixture should not produce pass
	// silently. Asserting on the findings directly, through the real
	// Validator, closes that gap: this scenario must load completely clean.
	s := mustScenario(t, src)
	findings := provider.ValidateScenario(s, map[string]provider.Validator{providerName: Validator{}})
	require.Empty(t, findings, "a warn-policy entry combined with truncate_body must load with no findings at all")

	// newSim itself fails the test on any ERROR finding (see its doc
	// comment), so reaching the end of this function IS the assertion.
	newSim(t, src)
}
