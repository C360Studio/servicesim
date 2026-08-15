package provider

import (
	"strings"
	"testing"
)

func TestValidJobID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"exa shape", "run_9f2c1ab4e5d67890abcdef0123456789", true},
		{"tavily uuid shape", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"single character", "a", true},
		{"underscores and dashes", "a_b-c", true},
		{"at the length bound", strings.Repeat("a", MaxJobIDLen), true},

		{"empty", "", false},
		{"one over the length bound", strings.Repeat("a", MaxJobIDLen+1), false},

		// The percent-decoded separator is the whole reason this exists: ServeMux
		// hands "run%2Fabc" to PathValue as "run/abc", and a lane key joins its
		// namespace with '/'.
		{"embedded slash", "run/abc", false},
		{"leading slash", "/run", false},
		{"the lane key separator", "run|abc", false},
		{"dot, which a path join would treat as traversal", "..", false},
		{"space", "run abc", false},
		{"newline, which a log parser would misread", "run\nabc", false},
		{"null byte", "run\x00abc", false},
		{"non-ascii", "rün", false},
		{"percent, undecoded", "run%2Fabc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidJobID(tc.id); got != tc.want {
				t.Errorf("ValidJobID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// A valid identifier must never be the value appendLanePart drops for length,
// or a legal poll would fall back to the shared route lane and two jobs would
// answer from one cursor. This pins the relationship rather than the numbers.
func TestValidJobIDFitsInALaneValue(t *testing.T) {
	t.Parallel()

	if MaxJobIDLen >= maxLaneValueLen {
		t.Fatalf("MaxJobIDLen (%d) must be below maxLaneValueLen (%d), or a valid identifier can be dropped from its own lane key",
			MaxJobIDLen, maxLaneValueLen)
	}
}

// ValidJobID must reject everything ValidNamespace does, because both feed the
// same lane key and the key is only unambiguous if neither half can carry a
// separator.
func TestValidJobIDRejectsEverySeparator(t *testing.T) {
	t.Parallel()

	for _, sep := range []string{namespaceSeparator, laneKeySeparator, "/", "|"} {
		id := "run" + sep + "abc"
		if ValidJobID(id) {
			t.Errorf("ValidJobID(%q) = true; a separator in an identifier lets a lane key be re-split", id)
		}
	}
}
