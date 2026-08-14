package ids

import (
	"fmt"
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes taken from the verified contract files, not from memory:
// contracts/exa/exa-search-400.json ("67207943fab9832d162b5317f4cca830"),
// contracts/perplexity/perplexity-agent-happy.json ("resp_…", "msg_…") and
// Tavily's documented request_id UUID.
var (
	hex32Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	uuidPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	respPattern  = regexp.MustCompile(`^resp_[0-9a-f]{32}$`)
	msgPattern   = regexp.MustCompile(`^msg_[0-9a-f]{32}$`)
	// Tavily's documented results[].id example is "a3f9c2-04".
	tavilyResultPattern = regexp.MustCompile(`^[0-9a-f]{6}-[0-9]{2}$`)
)

// tuple mirrors §3.1's unplanned shape: (seed key, provider, fault key).
func tuple() []string { return []string{"golden-seed", "exa", "exa:/search"} }

func TestDeriveIsDeterministic(t *testing.T) {
	first := Derive(tuple()...)
	for i := 0; i < 64; i++ {
		require.Equal(t, first, Derive(tuple()...), "Derive must not vary between calls")
	}
}

func TestDeriveDistinguishesPartBoundaries(t *testing.T) {
	// Every pair below concatenates to the same byte string. Without length
	// prefixing each pair would collide, which is the failure §3.1 calls out.
	pairs := [][2][]string{
		{{"ab", "c"}, {"a", "bc"}},
		{{"exa", "search"}, {"exas", "earch"}},
		{{"exasearch"}, {"exa", "search"}},
		{{"a", "", "b"}, {"a", "b", ""}},
		{{""}, {}},
	}
	for _, pair := range pairs {
		t.Run(fmt.Sprintf("%q_vs_%q", pair[0], pair[1]), func(t *testing.T) {
			assert.NotEqual(t, Derive(pair[0]...), Derive(pair[1]...))
			assert.NotEqual(t, Hex32(pair[0]...), Hex32(pair[1]...))
			assert.NotEqual(t, UUIDv5(pair[0]...), UUIDv5(pair[1]...))
		})
	}
}

func TestDeriveVariesWithEveryPart(t *testing.T) {
	base := Derive("seed", "exa", "exa:/search")
	assert.NotEqual(t, base, Derive("seed2", "exa", "exa:/search"))
	assert.NotEqual(t, base, Derive("seed", "tavily", "exa:/search"))
	assert.NotEqual(t, base, Derive("seed", "exa", "exa:/answer"))
	// The planned shape appends an attempt index, so it must differ from the
	// unplanned one and from every other attempt.
	assert.NotEqual(t, base, Derive("seed", "exa", "exa:/search", "0"))
	assert.NotEqual(t, Derive("seed", "exa", "exa:/search", "0"), Derive("seed", "exa", "exa:/search", "1"))
}

func TestHex32MatchesExaRequestIDShape(t *testing.T) {
	// The vendor sample is only a shape reference; what matters is that a
	// derived value is indistinguishable from it by length and alphabet.
	const vendorSample = "67207943fab9832d162b5317f4cca830"
	require.Regexp(t, hex32Pattern, vendorSample, "vendor sample must itself match the asserted shape")

	for _, parts := range [][]string{
		{},
		{""},
		{"golden-seed", "exa", "exa:/search"},
		{"golden-seed", "exa", "exa:/answer", "3"},
		{"seed with spaces", "exa", "exa:/search"},
		{"ünïcode-seed", "exa", "exa:/search"},
	} {
		got := Hex32(parts...)
		assert.Regexp(t, hex32Pattern, got)
		assert.Len(t, got, len(vendorSample))
	}
}

func TestHex32IsPinned(t *testing.T) {
	// Pinning the exact strings turns "we changed the derivation" from a silent
	// golden-file churn into a failing test that names the cause.
	assert.Equal(t, "f6d1be191056db0bb37501d374a8b338", Hex32("demo", "exa", "exa:/search"))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb924", Hex32())
}

func TestUUIDv5MatchesRFC4122Version5(t *testing.T) {
	for _, parts := range [][]string{
		{},
		{"golden-seed", "tavily", "tavily:/search"},
		{"golden-seed", "perplexity", "perplexity:/chat/completions", "2"},
	} {
		got := UUIDv5(parts...)
		assert.Regexp(t, uuidPattern, got, "UUID must carry version 5 and the RFC 4122 variant")
		assert.Len(t, got, 36)
	}
}

func TestUUIDv5IsPinned(t *testing.T) {
	// The namespace is itself derived (version 5 of "servicesim.c360studio.test"
	// under the DNS namespace); pinning it stops an edit to that name from
	// silently rewriting every Tavily request_id in every consumer's golden.
	assert.Equal(t,
		"0a82b973-3f30-5864-943f-ab48520a4e72",
		fmt.Sprintf("%x-%x-%x-%x-%x", namespace[0:4], namespace[4:6], namespace[6:8], namespace[8:10], namespace[10:16]),
	)
	assert.Equal(t, "00ce708b-e383-53f8-a9be-2bfe118b053d", UUIDv5("demo", "tavily", "tavily:/search"))
}

func TestUUIDv5IsDeterministic(t *testing.T) {
	first := UUIDv5(tuple()...)
	for i := 0; i < 64; i++ {
		require.Equal(t, first, UUIDv5(tuple()...))
	}
}

func TestPerplexityAgentIdentifierShapes(t *testing.T) {
	// The provider package applies the prefixes (extended-surfaces "Rendering
	// rules"); this asserts the hex body is the right size for them.
	parts := []string{"golden-seed", "perplexity", "agent", "perplexity:/v1/responses"}
	responseID := "resp_" + Hex32(parts...)
	messageID := "msg_" + Hex32(append(append([]string{}, parts...), "message")...)

	assert.Regexp(t, respPattern, responseID)
	assert.Regexp(t, msgPattern, messageID)
	assert.Len(t, responseID, len("resp_9f2c1d8b7a6e5f4c3b2a1908f7e6d5c4"))
	assert.Len(t, messageID, len("msg_4e3d2c1b0a9f8e7d6c5b4a39281706f5"))
	assert.NotEqual(t, responseID[5:], messageID[4:], "the message id must not repeat the response id")
}

func TestTavilyResultIdentifierShape(t *testing.T) {
	// §3.1: ids.Hex32(seed, "tavily", sourceID)[:8] shaped as xxxxxx-NN.
	for i, sourceID := range []string{"src-a", "src-b", "src-c"} {
		got := fmt.Sprintf("%s-%02d", Hex32("golden-seed", "tavily", sourceID)[:6], i)
		assert.Regexp(t, tavilyResultPattern, got)
		assert.Len(t, got, len("a3f9c2-04"))
	}
}

func TestFloatStaysInRange(t *testing.T) {
	const lo, hi = 0.25, 0.95
	for i := 0; i < 5000; i++ {
		got := Float(lo, hi, "golden-seed", "exa", fmt.Sprintf("source-%d", i))
		require.GreaterOrEqual(t, got, lo)
		require.Less(t, got, hi, "the range is half-open, so hi itself is never produced")
	}
}

func TestFloatIsDeterministic(t *testing.T) {
	first := Float(0, 1, tuple()...)
	for i := 0; i < 64; i++ {
		require.Equal(t, first, Float(0, 1, tuple()...))
	}
	assert.NotEqual(t, first, Float(0, 1, "other-seed", "exa", "exa:/search"))
}

func TestFloatDegenerateRangeReturnsLo(t *testing.T) {
	assert.Equal(t, 0.5, Float(0.5, 0.5, tuple()...))
	assert.Equal(t, 0.9, Float(0.9, 0.1, tuple()...))
}

func TestNoCollisionsAcrossRealisticTuples(t *testing.T) {
	hexes := make(map[string]string, 30000)
	uuids := make(map[string]string, 30000)
	for _, provider := range []string{"exa", "tavily", "perplexity"} {
		for _, route := range []string{"search", "answer", "contents", "chat", "agent"} {
			for attempt := 0; attempt < 2000; attempt++ {
				key := fmt.Sprintf("%s/%s/%d", provider, route, attempt)
				parts := []string{"golden-seed", provider, route, fmt.Sprint(attempt)}

				// Plain map lookups, not require.NotContains: the latter scans
				// the whole map per call, which turns this into a quadratic test.
				h := Hex32(parts...)
				if prev, dup := hexes[h]; dup {
					require.FailNowf(t, "Hex32 collision", "%s and %s both derive %s", prev, key, h)
				}
				hexes[h] = key

				u := UUIDv5(parts...)
				if prev, dup := uuids[u]; dup {
					require.FailNowf(t, "UUIDv5 collision", "%s and %s both derive %s", prev, key, u)
				}
				uuids[u] = key
			}
		}
	}
}

func TestConcurrentDerivationIsStable(t *testing.T) {
	// Derivation must hold no shared mutable state: the simulator derives ids
	// on concurrent request paths, and a data race here would surface as a
	// run-to-run-different golden rather than as an obvious crash.
	wantHex := Hex32(tuple()...)
	wantUUID := UUIDv5(tuple()...)
	wantFloat := Float(0, 1, tuple()...)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Equal(t, wantHex, Hex32(tuple()...))
			assert.Equal(t, wantUUID, UUIDv5(tuple()...))
			assert.Equal(t, wantFloat, Float(0, 1, tuple()...))
		}()
	}
	wg.Wait()
}
