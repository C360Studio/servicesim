package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// mustParse parses src and fails t if the envelope itself does not
// validate — every test in this file is about a Validator finding, not an
// envelope-level one.
func mustParse(t *testing.T, src string) *scenario.Scenario {
	t.Helper()
	s, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)
	return s
}

// findingsFor runs Validator against the "mcp" entry and returns its
// findings.
func findingsFor(t *testing.T, s *scenario.Scenario) []scenario.Finding {
	t.Helper()
	return Validator{}.ValidateProjections(s, s.Provider(string(Name)))
}

func hasCode(findings []scenario.Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func severityOf(findings []scenario.Finding, code string) scenario.Severity {
	for _, f := range findings {
		if f.Code == code {
			return f.Severity
		}
	}
	return ""
}

func TestValidatorToolNameOutsidePatternIsWarning(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: "has a space"
        input_schema: {type: object}
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeToolNameInvalid))
	require.Equal(t, scenario.SeverityWarning, severityOf(findings, CodeToolNameInvalid))
}

func TestValidatorDuplicateToolNameIsError(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
        input_schema: {type: object}
      - name: search
        input_schema: {type: object}
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeToolNameDuplicate))
	require.Equal(t, scenario.SeverityError, severityOf(findings, CodeToolNameDuplicate))
}

func TestValidatorInputSchemaMustBeObjectType(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
        input_schema: {type: array}
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeToolInputSchemaInvalid))
	require.Equal(t, scenario.SeverityError, severityOf(findings, CodeToolInputSchemaInvalid))
}

func TestValidatorInputSchemaAbsentIsFine(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
`)
	findings := findingsFor(t, s)
	require.False(t, hasCode(findings, CodeToolInputSchemaInvalid))
}

func TestValidatorXMcpHeaderAnywhereInSchemaIsError(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
        input_schema:
          type: object
          properties:
            query:
              type: string
              x-mcp-header: Query
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeToolXMcpHeaderUnsupported))
	require.Equal(t, scenario.SeverityError, severityOf(findings, CodeToolXMcpHeaderUnsupported))
}

func TestValidatorOutputSchemaWithStructuredContentIsWarningOnce(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
        input_schema: {type: object}
        output_schema: {type: object}
    results:
      search:
        structured_content: {a: 1}
`)
	findings := findingsFor(t, s)
	count := 0
	for _, f := range findings {
		if f.Code == CodeToolOutputSchemaUnchecked {
			count++
			require.Equal(t, scenario.SeverityWarning, f.Severity)
		}
	}
	require.Equal(t, 1, count)
}

func TestValidatorOutputSchemaWithoutStructuredContentDoesNotWarn(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: search
        input_schema: {type: object}
        output_schema: {type: object}
    results:
      search:
        content: [{type: text, text: hi}]
`)
	findings := findingsFor(t, s)
	require.False(t, hasCode(findings, CodeToolOutputSchemaUnchecked))
}

func TestValidatorStreamRejectIsMeaninglessLoadError(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    stream: {when_requested: reject}
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeStreamRejectMeaningless))
	require.Equal(t, scenario.SeverityError, severityOf(findings, CodeStreamRejectMeaningless))
}

func TestValidatorUnknownContentTypeIsError(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    results:
      search:
        content: [{type: bogus, text: hi}]
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeContentTypeUnknown))
}

func TestValidatorUnknownSourceReferenceIsError(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    results:
      search:
        content: [{type: text, source: no-such-source}]
`)
	findings := findingsFor(t, s)
	require.True(t, hasCode(findings, CodeSourceUnknown))
}

func TestValidatorRouteListerRejectsUnknownRouteName(t *testing.T) {
	t.Parallel()
	s := mustParse(t, `
version: 1
name: v
providers:
  mcp:
    turns:
      - when: {route: bogus}
        respond: {}
`)
	findings := provider.ValidateScenario(s, map[string]provider.Validator{string(Name): Validator{}})
	require.True(t, hasCode(findings, provider.CodeTurnRouteUnknown))
}

func TestRoutesReturnsTheOneRoute(t *testing.T) {
	t.Parallel()
	routes := Routes()
	require.Len(t, routes, 1)
	require.Equal(t, PatternMCP, routes[0].Pattern)
	require.Equal(t, FaultKeyMCP, routes[0].FaultKey)
}
