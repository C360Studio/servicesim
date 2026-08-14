package scenario

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, src string) *Scenario {
	t.Helper()
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	return s
}

const twoSources = `version: 1
name: fusion
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: Example Author
    published_at: 2026-05-20T00:00:00.547Z
    snippets: [First excerpt., Second excerpt.]
    image: https://example.test/a.png
    favicon: https://example.test/a.ico
    text: Full source text.
    claims:
      - {id: claim-1, text: Shared claim.}
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    author_null: true
    claims:
      - {id: claim-1, text: "Shared claim, restated."}
      - {id: claim-2, text: Distinct claim.}
`

// TestResolve_IndexesAddressTheCorpusSlice guards the one loop the design says a
// reviewer should look at twice: ranging by value would index the per-iteration
// copy, so every projection would point at a value that is not in s.Sources and
// that mutations never reach.
func TestResolve_IndexesAddressTheCorpusSlice(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)

	got, ok := s.SourceByID("source-a")
	if !ok {
		t.Fatal("source-a not indexed")
	}
	if got != &s.Sources[0] {
		t.Fatal("the index must address the corpus slice element, not a copy")
	}
	s.Sources[0].Title = "Rewritten"
	if got.Title != "Rewritten" {
		t.Fatal("a mutation of the corpus must be visible through the index")
	}
	if _, ok := s.SourceByID("source-z"); ok {
		t.Error("SourceByID must report a miss")
	}
}

func TestSourcesForClaim_IsInCorpusDeclarationOrder(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)

	shared := s.SourcesForClaim("claim-1")
	if len(shared) != 2 {
		t.Fatalf("claim-1 asserted by %d sources, want 2", len(shared))
	}
	if shared[0] != &s.Sources[0] || shared[1] != &s.Sources[1] {
		t.Fatal("corroborating sources must come back in corpus declaration order")
	}
	if len(s.SourcesForClaim("claim-2")) != 1 {
		t.Error("claim-2 should have one source")
	}
	if s.SourcesForClaim("claim-none") != nil {
		t.Error("an unknown claim must answer nil, not panic")
	}
}

func TestResolve_RejectsDuplicateSourceIDs(t *testing.T) {
	t.Parallel()

	s := &Scenario{Version: SchemaVersion, Name: "n", Sources: []Source{
		{ID: "a", URL: "https://example.test/a", Title: "A"},
		{ID: "a", URL: "https://example.test/b", Title: "B"},
	}}
	err := s.Resolve()
	if err == nil || !strings.Contains(err.Error(), `duplicate source "a"`) {
		t.Fatalf("Resolve() = %v", err)
	}
}

type exaResultLike struct {
	SourceRef `yaml:",inline"`
	ID        string `yaml:"id,omitempty"`
}

type projectionLike struct {
	Results   []exaResultLike `yaml:"results,omitempty"`
	Citations []SourceRef     `yaml:"citations,omitempty"`
	Nested    *struct {
		Ref SourceRef `yaml:"ref"`
	} `yaml:"nested,omitempty"`
}

func TestResolveRefs_LinksEveryReference(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)
	proj := projectionLike{
		Results:   []exaResultLike{{SourceRef: SourceRef{Ref: "source-a"}}, {SourceRef: SourceRef{Ref: "source-b"}}},
		Citations: []SourceRef{{Ref: "source-b"}},
	}
	proj.Nested = &struct {
		Ref SourceRef `yaml:"ref"`
	}{Ref: SourceRef{Ref: "source-a"}}

	if findings := s.ResolveRefs("providers.exa", &proj); len(findings) != 0 {
		t.Fatalf("findings = %+v", findings)
	}
	if proj.Results[0].Target != &s.Sources[0] || proj.Results[1].Target != &s.Sources[1] {
		t.Fatal("inline references must resolve to the corpus elements")
	}
	if proj.Citations[0].Target != &s.Sources[1] {
		t.Fatal("a bare SourceRef slice must resolve")
	}
	if proj.Nested.Ref.Target != &s.Sources[0] {
		t.Fatal("a reference behind a pointer must resolve")
	}
}

func TestResolveRefs_UnknownReferenceNamesItsPath(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)
	proj := projectionLike{
		Results: []exaResultLike{
			{SourceRef: SourceRef{Ref: "source-a"}},
			{SourceRef: SourceRef{Ref: "source-b"}},
			{SourceRef: SourceRef{Ref: "source-z"}},
		},
	}
	findings := s.ResolveRefs("providers.exa", &proj)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	f := findings[0]
	if f.Severity != SeverityError || f.Code != "scenario.source.unknown" {
		t.Errorf("finding = %+v", f)
	}
	// The design's worked example of an actionable message.
	if f.Path != "providers.exa.results[2].source" {
		t.Errorf("path = %q, want providers.exa.results[2].source", f.Path)
	}
	if !strings.Contains(f.Message, `unknown source "source-z"`) {
		t.Errorf("message = %q", f.Message)
	}
}

func TestResolve_EmptyReference(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)
	proj := projectionLike{Citations: []SourceRef{{}}}
	findings := s.ResolveRefs("providers.perplexity", &proj)
	if len(findings) != 1 || findings[0].Code != "scenario.source.missing" {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0].Path != "providers.perplexity.citations[0].source" {
		t.Errorf("path = %q", findings[0].Path)
	}
}

func TestResolveRefs_NilSafe(t *testing.T) {
	t.Parallel()

	var s *Scenario
	if f := s.ResolveRefs("x", &projectionLike{}); f != nil {
		t.Errorf("nil scenario: %+v", f)
	}
	loaded := mustParse(t, twoSources)
	if f := loaded.ResolveRefs("x", nil); f != nil {
		t.Errorf("nil value: %+v", f)
	}
	var missing *projectionLike
	if f := loaded.ResolveRefs("x", missing); f != nil {
		t.Errorf("nil pointer: %+v", f)
	}
}

func TestRender_AppliesDefaults(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)
	a, _ := s.SourceByID("source-a")
	b, _ := s.SourceByID("source-b")

	got := Render(SourceRef{Ref: "source-a", Target: a})
	if got.ID != "source-a" || got.URL != "https://example.test/report-a" || got.Title != "Report A" {
		t.Fatalf("rendered = %+v", got)
	}
	if author, ok := got.Author.Get(); !ok || author != "Example Author" {
		t.Errorf("author = %q, %v", author, ok)
	}
	// Millisecond precision matches Exa's own example; a parser tuned to whole
	// seconds would reject it, which is exactly what a consumer should discover
	// in a test rather than in production.
	if published, ok := got.PublishedAt.Get(); !ok || published != "2026-05-20T00:00:00.547Z" {
		t.Errorf("published_at = %q, %v", published, ok)
	}
	if got.FirstSnippet() != "First excerpt." {
		t.Errorf("FirstSnippet = %q", got.FirstSnippet())
	}
	if got.Image == "" || got.Favicon == "" || got.Text == "" {
		t.Errorf("optional fields dropped: %+v", got)
	}

	// author_null forces the explicit-null branch a consumer would otherwise
	// never exercise.
	nulled := Render(SourceRef{Ref: "source-b", Target: b})
	if !nulled.Author.IsNull() {
		t.Errorf("author = %+v, want an explicit null", nulled.Author)
	}
	if !nulled.PublishedAt.IsZero() {
		t.Errorf("published_at = %+v, want absent", nulled.PublishedAt)
	}
	if nulled.FirstSnippet() != "" {
		t.Errorf("FirstSnippet = %q, want empty", nulled.FirstSnippet())
	}

	// An unresolved reference must degrade, never dereference nil on a request
	// path.
	unresolved := Render(SourceRef{Ref: "source-z"})
	if unresolved.ID != "source-z" || unresolved.URL != "" {
		t.Errorf("unresolved = %+v", unresolved)
	}
}

func TestRender_IsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	s := mustParse(t, twoSources)
	a, _ := s.SourceByID("source-a")
	ref := SourceRef{Ref: "source-a", Target: a}

	first := Render(ref)
	for range 20 {
		if got := Render(ref); got.ID != first.ID || got.URL != first.URL {
			t.Fatal("Render must be a pure function of the source")
		}
	}

	// Nothing in a rendered timestamp comes from a clock.
	pinned := time.Date(1999, time.December, 31, 23, 59, 59, 0, time.UTC)
	a.PublishedAt = &pinned
	if published, _ := Render(ref).PublishedAt.Get(); published != "1999-12-31T23:59:59.000Z" {
		t.Fatalf("published_at = %q", published)
	}
}
