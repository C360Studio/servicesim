package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// parseNode decodes src into a bare *yaml.Node rooted at the document's
// mapping, for tests that exercise resolveAliases directly rather than
// through the Scenario/Providers pipeline.
func parseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		t.Fatalf("unexpected document shape: %+v", doc)
	}
	return doc.Content[0]
}

// mapValue returns the value node for key in mapping m, failing the test if
// absent.
func mapValue(t *testing.T, m *yaml.Node, key string) *yaml.Node {
	t.Helper()
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	t.Fatalf("key %q not found in mapping", key)
	return nil
}

// TestResolveAliases_ScalarAlias covers an alias to a bare scalar.
func TestResolveAliases_ScalarAlias(t *testing.T) {
	t.Parallel()

	root := parseNode(t, "a: &s hello\nb: *s\n")
	resolved, err := resolveAliases(root)
	if err != nil {
		t.Fatalf("resolveAliases: %v", err)
	}

	b := mapValue(t, resolved, "b")
	if b.Kind != yaml.ScalarNode {
		t.Fatalf("b.Kind = %v, want ScalarNode", b.Kind)
	}
	if b.Value != "hello" {
		t.Errorf("b.Value = %q, want %q", b.Value, "hello")
	}
	if b.Anchor != "" {
		t.Errorf("b.Anchor = %q, want empty: the copy must not redeclare the anchor", b.Anchor)
	}
}

// TestResolveAliases_SequenceAlias covers an alias to a sequence, the shape a
// shared sources list would take.
func TestResolveAliases_SequenceAlias(t *testing.T) {
	t.Parallel()

	root := parseNode(t, "a: &lst\n  - x\n  - y\nb: *lst\n")
	resolved, err := resolveAliases(root)
	if err != nil {
		t.Fatalf("resolveAliases: %v", err)
	}

	b := mapValue(t, resolved, "b")
	if b.Kind != yaml.SequenceNode {
		t.Fatalf("b.Kind = %v, want SequenceNode", b.Kind)
	}
	if len(b.Content) != 2 || b.Content[0].Value != "x" || b.Content[1].Value != "y" {
		t.Errorf("b sequence = %+v", b.Content)
	}
	if b.Anchor != "" {
		t.Errorf("b.Anchor = %q, want empty", b.Anchor)
	}

	// The alias's copy must own an independently allocated Content slice: a
	// mutation through one side must not reach the other.
	a := mapValue(t, resolved, "a")
	b.Content[0].Value = "mutated"
	if a.Content[0].Value == "mutated" {
		t.Error("mutating the alias copy's sequence also mutated the anchor's own — Content slice was shared, not copied")
	}
}

// TestResolveAliases_MappingAlias covers an alias to a nested mapping — the
// §2.1 shape (docs/design/async-jobs.md) of a repeated poll snapshot.
func TestResolveAliases_MappingAlias(t *testing.T) {
	t.Parallel()

	root := parseNode(t, "a: &m\n  status: running\nb: *m\n")
	resolved, err := resolveAliases(root)
	if err != nil {
		t.Fatalf("resolveAliases: %v", err)
	}

	b := mapValue(t, resolved, "b")
	if b.Kind != yaml.MappingNode {
		t.Fatalf("b.Kind = %v, want MappingNode", b.Kind)
	}
	if got := mapValue(t, b, "status"); got.Value != "running" {
		t.Errorf("b.status = %q, want %q", got.Value, "running")
	}
	if b.Anchor != "" {
		t.Errorf("b.Anchor = %q, want empty", b.Anchor)
	}

	// Independent Content slice, same as the sequence case: mutating the
	// alias's copy must not reach the anchor's own mapping.
	a := mapValue(t, resolved, "a")
	mapValue(t, b, "status").Value = "mutated"
	if mapValue(t, a, "status").Value == "mutated" {
		t.Error("mutating the alias copy's mapping also mutated the anchor's own")
	}
}

// TestResolveAliases_SetsAliasPositionNotAnchorPosition is §2.1's own load-time
// finding requirement made a direct, white-box assertion: the resolved copy's
// Line and Column must be the ALIAS node's, not the anchor's, so a later
// finding raised against that node addresses the turn that used the alias.
func TestResolveAliases_SetsAliasPositionNotAnchorPosition(t *testing.T) {
	t.Parallel()

	src := "a: &m\n  status: running\n" + // anchor on line 1-2
		"unrelated: 1\n" + // line 3, pushes the alias further down
		"b: *m\n" // alias on line 4
	root := parseNode(t, src)

	anchor := mapValue(t, root, "a")
	resolved, err := resolveAliases(root)
	if err != nil {
		t.Fatalf("resolveAliases: %v", err)
	}
	b := mapValue(t, resolved, "b")

	if b.Line == anchor.Line {
		t.Fatalf("b.Line = %d, same as the anchor's (%d) — the fixture did not separate them", b.Line, anchor.Line)
	}
	if b.Line != 4 {
		t.Errorf("b.Line = %d, want 4 (the alias's own line)", b.Line)
	}
}

// TestResolveAliases_NestedAnchorsAreClearedInEveryCopy covers a nested
// anchor inside an aliased subtree: `a: &m {x: &n 1, y: *n}` aliased twice as
// `p: *m, q: *m`. Both copies of m's subtree must have their nested n anchor
// cleared, not just m's own root anchor — otherwise yaml.Marshal is willing
// to re-emit "&n" from either copy, and DecodeStrict's later re-marshal of
// some unrelated node that happens to also read an anchor named "n" would
// resolve against whichever copy was produced most recently instead of
// failing loudly.
func TestResolveAliases_NestedAnchorsAreClearedInEveryCopy(t *testing.T) {
	t.Parallel()

	root := parseNode(t, "a: &m\n  x: &n 1\n  y: *n\np: *m\nq: *m\n")
	resolved, err := resolveAliases(root)
	if err != nil {
		t.Fatalf("resolveAliases: %v", err)
	}

	for _, key := range []string{"p", "q"} {
		copyNode := mapValue(t, resolved, key)
		if copyNode.Anchor != "" {
			t.Errorf("%s.Anchor = %q, want empty", key, copyNode.Anchor)
		}
		x := mapValue(t, copyNode, "x")
		if x.Anchor != "" {
			t.Errorf("%s.x.Anchor = %q, want empty — nested anchor was not cleared", key, x.Anchor)
		}
		y := mapValue(t, copyNode, "y")
		if y.Kind != yaml.ScalarNode || y.Value != "1" {
			t.Errorf("%s.y = %+v, want resolved scalar \"1\"", key, y)
		}
	}
}

// TestResolveAliases_CyclicAliasReportsErrorRatherThanHanging documents that
// yaml.v3 v3.0.1 does NOT reject a self-referencing anchor at parse time —
// confirmed here rather than assumed — and proves resolveAliases both
// terminates against one and reports it as an error, rather than silently
// returning the raw AliasNode unresolved into the tree (which would leave an
// alias for a downstream decoder to trip over instead of the loader
// reporting it at the alias's own site).
func TestResolveAliases_CyclicAliasReportsErrorRatherThanHanging(t *testing.T) {
	t.Parallel()

	src := "a: &x\n  b: *x\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	root := doc.Content[0]

	// Confirm the cycle is real: the alias node's own Alias pointer closes back
	// on the mapping that contains it.
	a := mapValue(t, root, "a")
	b := mapValue(t, a, "b")
	if b.Kind != yaml.AliasNode || b.Alias != a {
		t.Fatalf("fixture does not construct the cycle this test needs: b.Kind=%v b.Alias==a is %v", b.Kind, b.Alias == a)
	}

	type result struct {
		node *yaml.Node
		err  error
	}
	done := make(chan result, 1)
	go func() {
		node, err := resolveAliases(root)
		done <- result{node, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatalf("resolveAliases: want an error for a cyclic anchor, got nil (node=%+v)", r.node)
		}
		if !strings.Contains(r.err.Error(), "contains itself") {
			t.Errorf("resolveAliases error = %q, want it to name the self-referencing anchor", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolveAliases did not return within 5s — the cyclic anchor recursed without bound")
	}
}

// TestResolveAliases_ExcessiveFanOutIsBounded covers the non-cyclic case
// finding 2 raised against an earlier version of this file: pre-expanding
// aliases before any decoder sees the tree bypasses yaml.v3's own
// "document contains excessive aliasing" guard, so resolveAliases needs its
// own budget. Eight nested anchors of fan-out eight would expand to 8^8
// (~16.7M) leaf nodes uncaught; this must fail fast with an error rather than
// hang or exhaust memory.
func TestResolveAliases_ExcessiveFanOutIsBounded(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("l0: &l0 [a, b, c, d, e, f, g, h]\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "l%d: &l%d [*l%d, *l%d, *l%d, *l%d, *l%d, *l%d, *l%d, *l%d]\n",
			i, i, i-1, i-1, i-1, i-1, i-1, i-1, i-1, i-1)
	}
	root := parseNode(t, b.String())

	done := make(chan error, 1)
	go func() {
		_, err := resolveAliases(root)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("resolveAliases: want a budget error for pathological fan-out, got nil")
		}
		if !strings.Contains(err.Error(), "exceeded") {
			t.Errorf("resolveAliases error = %q, want it to name the exceeded budget", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resolveAliases did not return within 10s — the fan-out budget did not stop it")
	}
}

// TestParse_AliasedRespondAcrossTurns is docs/design/async-jobs.md §2.1's own
// shape: an anchored respond on one turn, aliased by the next, then an
// unconditional terminal turn. It must load with no findings, and the two
// aliased turns must decode to the same content the anchor carries.
func TestParse_AliasedRespondAcrossTurns(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: exa-agent-run-completes
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: The report states the finding.
providers:
  exa_agent_runs:
    auth:
      mode: required
    turns:
      - when: {call_index: 0}
        respond: &pending
          status: running
      - when: {call_index: 1}
        respond: *pending
      - respond:
          status: completed
          output:
            text: Report A states the finding.
`
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", report.Findings)
	}

	e := s.Providers.Get("exa_agent_runs")
	if e == nil {
		t.Fatal("exa_agent_runs entry missing")
	}
	if len(e.Turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(e.Turns))
	}

	type snapshot struct {
		Status string `yaml:"status"`
	}
	for i, want := range []string{"running", "running", "completed"} {
		var got snapshot
		if err := e.Turns[i].Respond.Decode(&got); err != nil {
			t.Fatalf("turns[%d].respond.Decode: %v", i, err)
		}
		if got.Status != want {
			t.Errorf("turns[%d].status = %q, want %q", i, got.Status, want)
		}
		if e.Turns[i].Respond.Kind != yaml.MappingNode {
			t.Errorf("turns[%d].respond.Kind = %v, want MappingNode", i, e.Turns[i].Respond.Kind)
		}
	}
}

// TestParse_AnchorAcrossProviderBlocks covers an anchor defined in one
// provider entry and aliased from a different, later provider entry — the
// widest legal span within `providers:`.
//
// Both entries use the multi-turn form (`turns:`) deliberately, not the
// single-shot form: a single-shot block's body never reaches decodeTurns or
// DecodeStrict, so it cannot exercise DecodeStrict's single-node re-marshal —
// the actual mechanism the bug this file fixes was in.
func TestParse_AnchorAcrossProviderBlocks(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: cross-provider-anchor
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - respond: &shared_status
          status: running
  tavily_research:
    turns:
      - respond: *shared_status
`
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", report.Findings)
	}

	type snapshot struct {
		Status string `yaml:"status"`
	}
	for _, name := range []string{"exa_agent_runs", "tavily_research"} {
		e := s.Providers.Get(name)
		if e.Turns[0].Respond.Kind != yaml.MappingNode {
			t.Fatalf("%s: turns[0].respond.Kind = %v, want MappingNode", name, e.Turns[0].Respond.Kind)
		}
		var got snapshot
		if err := e.Turns[0].Respond.Decode(&got); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if got.Status != "running" {
			t.Errorf("%s: status = %q, want %q", name, got.Status, "running")
		}
	}
}

// TestParse_AnchorOutsideProvidersAliasedInsideProvider covers an anchor
// defined outside providers entirely — in sources: — and aliased inside a
// provider block's turn. Aliases are resolved over the whole parsed document
// graph, not just the providers: subtree, so this must work with no special
// casing.
//
// The multi-turn form is deliberate here too, for the same reason as
// TestParse_AnchorAcrossProviderBlocks: only decodeTurns' DecodeStrict call
// exercises the single-node re-marshal the bug was in.
func TestParse_AnchorOutsideProvidersAliasedInsideProvider(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: anchor-outside-providers
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: &shared_author A. Author
providers:
  exa_agent_runs:
    turns:
      - respond:
          status: running
          note: *shared_author
`
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", report.Findings)
	}

	e := s.Providers.Get("exa_agent_runs")
	if e.Turns[0].Respond.Kind != yaml.MappingNode {
		t.Fatalf("turns[0].respond.Kind = %v, want MappingNode", e.Turns[0].Respond.Kind)
	}
	var got struct {
		Status string `yaml:"status"`
		Note   string `yaml:"note"`
	}
	if err := e.Turns[0].Respond.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Note != "A. Author" {
		t.Errorf("note = %q, want %q", got.Note, "A. Author")
	}

	// The corpus itself must be unaffected by the alias reaching into it.
	src2, ok := s.SourceByID("source-a")
	if !ok || src2.Author != "A. Author" {
		t.Errorf("source-a.author = %q, ok=%v", src2.Author, ok)
	}
}

// TestParse_AliasedTurnItemReportsAliasLineNotAnchorLine is the load-time
// finding test: a whole turn entry that IS an alias to a non-mapping value
// must be reported at the alias's own line, not the anchor's.
//
// The "got a scalar" assertion is doing real work here, not just the line
// checks: an unresolved AliasNode already carries its OWN source line before
// this file's fix (nodes always know where they were parsed from), so a line
// check alone would pass whether or not resolution ever ran. What only the
// fix changes is the reported KIND — "alias" pre-fix (DecodeStrict's Kind
// check firing on the raw, un-dereferenced AliasNode) versus "scalar"
// post-fix (firing on the resolved copy) — which is why that assertion is the
// one that actually distinguishes this test from a no-op.
func TestParse_AliasedTurnItemReportsAliasLineNotAnchorLine(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: alias-line-attribution
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: &badturn A. Author
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - *badturn
`
	anchorLine := lineOf(t, src, "&badturn")
	aliasLine := lineOf(t, src, "- *badturn")
	if anchorLine == aliasLine {
		t.Fatalf("fixture does not separate the anchor (%d) from the alias (%d)", anchorLine, aliasLine)
	}

	_, report, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse: want an error for a turn that is not a mapping, got none")
	}
	if len(report.Errors()) != 1 {
		t.Fatalf("errors = %+v, want exactly one", report.Errors())
	}
	msg := report.Errors()[0].Message
	if !strings.Contains(msg, "turns[1]") {
		t.Errorf("message %q does not name turns[1]", msg)
	}
	wantLine := fmt.Sprintf("line %d:", aliasLine)
	if !strings.Contains(msg, wantLine) {
		t.Errorf("message %q does not contain %q (the alias's own line)", msg, wantLine)
	}
	badLine := fmt.Sprintf("line %d:", anchorLine)
	if strings.Contains(msg, badLine) {
		t.Errorf("message %q reports the anchor's line (%d) instead of the alias's (%d)", msg, anchorLine, aliasLine)
	}
	if !strings.Contains(msg, "got a scalar") {
		t.Errorf("message %q does not say %q — resolution did not run (see comment)", msg, "got a scalar")
	}
}

// TestParse_CyclicAnchorIsALoadTimeFinding is the end-to-end companion to
// TestResolveAliases_CyclicAliasReportsErrorRatherThanHanging: a scenario
// whose providers tree contains a self-referencing anchor must fail
// scenario.Parse with a finding naming the anchor, not decode successfully
// into a tree that still contains a live AliasNode for some later decoder to
// trip over with a less legible error.
func TestParse_CyclicAnchorIsALoadTimeFinding(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: cyclic-anchor
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - respond: &x
          status: running
          self: *x
`
	_, report, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse: want an error for a self-referencing anchor, got none")
	}
	if len(report.Errors()) != 1 {
		t.Fatalf("errors = %+v, want exactly one", report.Errors())
	}
	msg := report.Errors()[0].Message
	if !strings.Contains(msg, "contains itself") {
		t.Errorf("message %q does not report the self-referencing anchor", msg)
	}
}

// lineOf returns the 1-based line number of needle's first occurrence in src.
func lineOf(t *testing.T, src, needle string) int {
	t.Helper()
	idx := strings.Index(src, needle)
	if idx < 0 {
		t.Fatalf("needle %q not found in fixture", needle)
	}
	return strings.Count(src[:idx], "\n") + 1
}
