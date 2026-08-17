package profiles_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
)

// TestReference_NamesAndOrder pins the two-registration-lists claim
// (docs/proposals/framework-seam.md, "Add a two-line test that they are
// equal"): cmd/servicesim/main.go now calls profiles.Reference() directly,
// so the "two lists match" property collapses to Reference() itself
// returning exactly the four names, in the order main registers them.
func TestReference_NamesAndOrder(t *testing.T) {
	t.Parallel()

	ps := profiles.Reference()

	got := make([]provider.Name, 0, len(ps))
	for _, p := range ps {
		got = append(got, p.Name)
	}

	assert.Equal(t, []provider.Name{"exa", "tavily", "perplexity", "mcp"}, got)
}

// TestReference_NewSetSucceeds proves the four reference profiles compose
// into a valid registry: nothing about the move to profiles/<name> or the
// exported-surface trim broke NewSet's own validation of them.
func TestReference_NewSetSucceeds(t *testing.T) {
	t.Parallel()

	set, err := provider.NewSet(profiles.Reference()...)
	require.NoError(t, err)
	require.NotNil(t, set)
}
