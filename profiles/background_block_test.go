package profiles_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// TestBackgroundBlockLoadsOnlyWhereABackgroundLifecycleExists walks every entry
// kind the reference profiles register — read from the registry, so an entry
// added later is covered without anyone remembering to list it. A `background:`
// block loads only on an entry its profile names in
// provider.Profile.Backgroundable; on every other entry the framework rejects it
// with one code, because a block nothing reads would let its author believe they
// scripted a background run that can never happen.
func TestBackgroundBlockLoadsOnlyWhereABackgroundLifecycleExists(t *testing.T) {
	t.Parallel()

	const backgroundBlock = "    background:\n      turns:\n        - respond: {status: completed}\n"

	set := provider.MustSet(profiles.Reference()...)
	var optedIn []string
	for _, p := range set.All() {
		optedIn = append(optedIn, p.Backgroundable...)
	}
	// The one entry with a background lifecycle today. A profile that opts
	// another entry in changes this line on purpose; nothing opts in by
	// accident. assert, not require, so every entry's own verdict below is
	// still reported when this list is wrong.
	assert.Equal(t, []string{"perplexity_agent"}, optedIn)

	validators := set.Validators()
	kinds := set.EntryKinds()
	require.GreaterOrEqual(t, len(kinds), 7, "the registry lists every in-tree entry kind")

	for _, entry := range kinds {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()

			loads := slices.Contains(optedIn, entry)
			src := "version: 1\nname: n\nproviders:\n  " + entry + ":\n" + backgroundBlock
			s, report, err := scenario.Parse([]byte(src))
			require.NoError(t, err, "scenario-level load must accept the block on any entry: %+v", report.Findings)

			findings := provider.ValidateScenario(s, validators)

			var rejected []scenario.Finding
			for _, f := range findings {
				if f.Code == provider.CodeBackgroundUnsupported {
					rejected = append(rejected, f)
				}
			}
			if loads {
				assert.Empty(t, rejected)
				for _, f := range findings {
					assert.NotEqual(t, scenario.SeverityError, f.Severity, "unexpected error finding: %+v", f)
				}
				return
			}
			require.Len(t, rejected, 1, "findings: %+v", findings)
			assert.Equal(t, scenario.SeverityError, rejected[0].Severity, "a stray background: is a load error")
			assert.Equal(t, "providers."+entry+".background", rejected[0].Path)
			assert.Contains(t, rejected[0].Message, entry, "the message names the entry")
		})
	}
}
