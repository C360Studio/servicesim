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

// TestBackgroundCancelBlockLoadsOnlyWhereABackgroundCancelExists walks every
// entry kind the reference profiles register, as the background: walk beside it
// does, with a cancel: nested in the background: block. It loads only on an entry
// its profile names in provider.Profile.BackgroundCancellable; on an entry that is
// Backgroundable and not that, the nested block alone is rejected; and on every
// other entry the background: block is rejected and takes its cancel: with it —
// one finding, never two.
func TestBackgroundCancelBlockLoadsOnlyWhereABackgroundCancelExists(t *testing.T) {
	t.Parallel()

	const block = "    background:\n      turns:\n        - respond: {status: completed}\n" +
		"      cancel:\n        turns:\n          - respond: {status: cancelled}\n"

	set := provider.MustSet(profiles.Reference()...)
	var background, cancellable []string
	for _, p := range set.All() {
		background = append(background, p.Backgroundable...)
		cancellable = append(cancellable, p.BackgroundCancellable...)
	}
	// The one entry with a background cancel today. A profile that opts another
	// entry in changes this line on purpose. assert, not require, so every
	// entry's own verdict below is still reported when this list is wrong.
	assert.Equal(t, []string{"perplexity_agent"}, cancellable)

	validators := set.Validators()
	kinds := set.EntryKinds()
	require.GreaterOrEqual(t, len(kinds), 7, "the registry lists every in-tree entry kind")

	for _, entry := range kinds {
		t.Run(entry, func(t *testing.T) {
			t.Parallel()

			src := "version: 1\nname: n\nproviders:\n  " + entry + ":\n" + block
			s, report, err := scenario.Parse([]byte(src))
			require.NoError(t, err, "scenario-level load must accept the block on any entry: %+v", report.Findings)

			findings := provider.ValidateScenario(s, validators)
			var rejected, cancelRejected []scenario.Finding
			for _, f := range findings {
				switch f.Code {
				case provider.CodeBackgroundUnsupported:
					rejected = append(rejected, f)
				case provider.CodeBackgroundCancelUnsupported:
					cancelRejected = append(cancelRejected, f)
				}
			}

			switch {
			case slices.Contains(cancellable, entry):
				assert.Empty(t, rejected)
				assert.Empty(t, cancelRejected)
				for _, f := range findings {
					assert.NotEqual(t, scenario.SeverityError, f.Severity, "unexpected error finding: %+v", f)
				}
			case slices.Contains(background, entry):
				assert.Empty(t, rejected)
				require.Len(t, cancelRejected, 1, "findings: %+v", findings)
				assert.Equal(t, scenario.SeverityError, cancelRejected[0].Severity)
				assert.Equal(t, "providers."+entry+".background.cancel", cancelRejected[0].Path)
			default:
				require.Len(t, rejected, 1, "findings: %+v", findings)
				assert.Equal(t, "providers."+entry+".background", rejected[0].Path)
				assert.Empty(t, cancelRejected, "a rejected background: takes its cancel: with it")
			}
		})
	}
}
