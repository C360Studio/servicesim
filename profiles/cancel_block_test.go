package profiles_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// TestCancelBlockLoadsOnlyWhereACancelExists walks every in-tree entry. scenario
// decodes `cancel:` on any entry, because only a profile knows which of its
// entries have a cancel; so each profile must reject the block on an entry that
// has none — a block nothing reads would let an author believe they scripted a
// cancellation that can never happen. exa_agent_runs is the one entry with a
// poll lifecycle that a cancel can act on. tavily_research has a poll lifecycle
// but Tavily documents no cancel, so it is rejected too.
func TestCancelBlockLoadsOnlyWhereACancelExists(t *testing.T) {
	t.Parallel()

	const cancelBlock = "    cancel:\n      turns:\n        - respond: {status: cancelled}\n"

	tests := []struct {
		entry    string
		wantCode string // empty: the block loads
	}{
		{entry: "exa", wantCode: "exa.cancel.unsupported"},
		{entry: "exa_agent_runs"},
		{entry: "tavily", wantCode: "tavily.cancel.unsupported"},
		{entry: "tavily_research", wantCode: "tavily.cancel.unsupported"},
		{entry: "perplexity", wantCode: "perplexity.cancel.unsupported"},
		{entry: "perplexity_agent", wantCode: "perplexity.cancel.unsupported"},
		{entry: "mcp", wantCode: "mcp.cancel.unsupported"},
	}

	validators := provider.MustSet(profiles.Reference()...).Validators()

	for _, tc := range tests {
		t.Run(tc.entry, func(t *testing.T) {
			t.Parallel()

			src := "version: 1\nname: n\nproviders:\n  " + tc.entry + ":\n" + cancelBlock
			if tc.entry == "exa_agent_runs" {
				src += "    turns:\n      - respond: {status: completed, output: {text: done}}\n"
			}
			s, report, err := scenario.Parse([]byte(src))
			require.NoError(t, err, "scenario-level load must accept the block on any entry: %+v", report.Findings)

			findings := provider.ValidateScenario(s, validators)

			var rejected []scenario.Finding
			for _, f := range findings {
				if f.Code == tc.wantCode {
					rejected = append(rejected, f)
				}
			}
			if tc.wantCode == "" {
				for _, f := range findings {
					assert.NotEqual(t, scenario.SeverityError, f.Severity, "unexpected error finding: %+v", f)
				}
				return
			}
			require.Len(t, rejected, 1, "findings: %+v", findings)
			assert.Equal(t, scenario.SeverityError, rejected[0].Severity, "a stray cancel: is a load error")
			assert.Equal(t, "providers."+tc.entry+".cancel", rejected[0].Path)
			assert.Contains(t, rejected[0].Message, tc.entry, "the message names the entry")
		})
	}
}
