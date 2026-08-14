package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestLoad_PlanExampleStillLoadsUnchanged is the compatibility guard for the open
// provider registry. The YAML in testdata/plan-example.yaml is copied verbatim
// from docs/architecture-and-implementation-plan.md; the whole point of stripping
// reserved envelope keys rather than declaring a closed struct is that this file
// keeps loading with no edits.
func TestLoad_PlanExampleStillLoadsUnchanged(t *testing.T) {
	t.Parallel()

	s, report, err := Load(filepath.Join("testdata", "plan-example.yaml"))
	if err != nil {
		t.Fatalf("Load: %v (%+v)", err, report.Findings)
	}
	if len(report.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %+v", report.Warnings())
	}
	if s.Name != "fusion-overlap" || s.SeedKey() != "fusion-overlap" {
		t.Errorf("name/seed = %q/%q", s.Name, s.SeedKey())
	}
	if got := s.Path(); !strings.HasSuffix(got, "plan-example.yaml") {
		t.Errorf("Path() = %q", got)
	}

	src, ok := s.SourceByID("source-a")
	if !ok {
		t.Fatal("source-a not indexed")
	}
	if src.PublishedAt == nil || !src.PublishedAt.Equal(time.Date(2026, time.May, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("published_at = %v", src.PublishedAt)
	}
	if got := s.SourcesForClaim("claim-1"); len(got) != 1 || got[0] != src {
		t.Errorf("SourcesForClaim = %v", got)
	}

	if got := s.Providers.Names(); strings.Join(got, ",") != "exa,tavily,perplexity" {
		t.Fatalf("provider order = %v, want declaration order", got)
	}

	// Every provider is a single-shot block, so each normalises to exactly one
	// unconditional turn whose respond body is the block verbatim.
	for _, name := range s.Providers.Names() {
		e := s.Providers.Get(name)
		if len(e.Turns) != 1 {
			t.Fatalf("%s: %d turns, want 1", name, len(e.Turns))
		}
		if e.Turns[0].When != nil {
			t.Errorf("%s: normalised turn must be unconditional", name)
		}
		if e.Turns[0].Respond.Kind != yaml.MappingNode {
			t.Errorf("%s: respond kind = %v", name, e.Turns[0].Respond.Kind)
		}
		if e.Kind != name {
			t.Errorf("%s: kind = %q, want the map key", name, e.Kind)
		}
	}

	// The plan's projection bodies survive intact, including the scalar
	// shorthand for source references.
	type perplexityBody struct {
		Answer        string      `yaml:"answer,omitempty"`
		Citations     []SourceRef `yaml:"citations,omitempty"`
		SearchResults []SourceRef `yaml:"search_results,omitempty"`
	}
	var body perplexityBody
	e := s.Providers.Get("perplexity")
	if err := e.Turns[0].DecodeProjection(e.Name, 0, &body); err != nil {
		t.Fatalf("DecodeProjection: %v", err)
	}
	if body.Answer != "A grounded answer citing Report A." {
		t.Errorf("answer = %q", body.Answer)
	}
	if len(body.Citations) != 1 || body.Citations[0].Ref != "source-a" {
		t.Errorf("citations = %+v", body.Citations)
	}
	if len(body.SearchResults) != 1 || body.SearchResults[0].Ref != "source-a" {
		t.Errorf("search_results = %+v", body.SearchResults)
	}

	// References inside a projection body resolve through ResolveRefs, which is
	// what the provider package calls after decoding.
	if findings := s.ResolveRefs("providers.perplexity", &body); len(findings) != 0 {
		t.Fatalf("ResolveRefs: %+v", findings)
	}
	if body.Citations[0].Target != src {
		t.Error("citation target must point at the corpus source, not a copy")
	}
}

func TestLoad_PlanFaultExampleStillLoadsUnchanged(t *testing.T) {
	t.Parallel()

	s, report, err := Load(filepath.Join("testdata", "plan-fault.yaml"))
	if err != nil {
		t.Fatalf("Load: %v (%+v)", err, report.Findings)
	}
	e := s.Providers.Get("tavily")
	f := e.Turns[0].Fault
	if !f.HasAttempts() || len(f.Attempts) != 2 {
		t.Fatalf("fault = %+v", f)
	}
	if f.Attempts[0].Status != 429 || f.Attempts[0].RetryAfter == nil || *f.Attempts[0].RetryAfter != 1 {
		t.Errorf("first attempt = %+v", f.Attempts[0])
	}
	if f.Attempts[0].EffectiveKind() != FaultStatus {
		t.Errorf("429 with no other mangling must infer kind status")
	}
	if f.Attempts[1].EffectiveKind() != FaultNone {
		t.Errorf("a trailing status 200 means no fault")
	}
	if !s.HasFaults() {
		t.Error("HasFaults() = false")
	}
}

func TestLoad_MultiTurnForm(t *testing.T) {
	t.Parallel()

	s, report, err := Load(filepath.Join("testdata", "turns.yaml"))
	if err != nil {
		t.Fatalf("Load: %v (%+v)", err, report.Findings)
	}
	e := s.Providers.Get("perplexity")
	if len(e.Turns) != 3 {
		t.Fatalf("%d turns, want 3", len(e.Turns))
	}
	if e.Turns[0].When == nil || e.Turns[0].When.CallIndex == nil || *e.Turns[0].When.CallIndex != 0 {
		t.Errorf("turn 0 when = %+v", e.Turns[0].When)
	}
	if e.Turns[1].When.BodyContains != "report-a" {
		t.Errorf("turn 1 when = %+v", e.Turns[1].When)
	}
	if !e.Turns[1].Fault.HasAttempts() {
		t.Error("a per-turn fault is what lets a script rate-limit one call")
	}
	if e.Turns[2].When != nil {
		t.Error("the fallback turn must be unconditional")
	}
	if got := e.TurnKey.Extractors(); strings.Join(got, "|") != "route|body_json:model" {
		t.Errorf("turn_key = %v", got)
	}
	if e.Auth == nil || e.Auth.Mode != AuthRequired {
		t.Errorf("auth = %+v", e.Auth)
	}

	// The Agent surface is an independent entry, declared after Sonar.
	agent := s.Providers.Get("perplexity_agent")
	if agent == nil || agent.Kind != "perplexity_agent" || len(agent.Turns) != 1 {
		t.Fatalf("perplexity_agent = %+v", agent)
	}
}

func TestParse_VersionIsPeekedBeforeTheStrictDecode(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("testdata", "version-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s, report, err := Parse(src)
	if err == nil {
		t.Fatal("a version this build does not support must fail")
	}
	if s != nil {
		t.Error("no scenario should be returned for an unsupported version")
	}
	errs := report.Errors()
	if len(errs) != 1 {
		t.Fatalf("want exactly one finding, got %+v", errs)
	}
	if errs[0].Code != "scenario.version.unsupported" {
		t.Errorf("code = %q", errs[0].Code)
	}
	// The whole point of peeking first: one sentence naming both versions,
	// not a wall of unknown-key errors from the strict decode.
	for _, want := range []string{"version 2", "version 1"} {
		if !strings.Contains(errs[0].Message, want) {
			t.Errorf("message %q does not mention %q", errs[0].Message, want)
		}
	}
}

func TestParse_Failures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		wantCode string
		wantMsg  string
	}{
		{
			name:     "no version",
			src:      "name: n\n",
			wantCode: "scenario.version.unreadable",
			wantMsg:  "declares no version",
		},
		{
			name:     "not yaml",
			src:      "version: 1\n\tbad indentation\n",
			wantCode: "scenario.version.unreadable",
			wantMsg:  "not valid YAML",
		},
		{
			name:     "unknown top-level key",
			src:      "version: 1\nname: n\nsorces: []\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  "field sorces not found",
		},
		{
			name:     "provider block is not a mapping",
			src:      "version: 1\nname: n\nproviders:\n  exa: [1, 2]\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  "provider block must be a mapping",
		},
		{
			name:     "duplicate provider",
			src:      "version: 1\nname: n\nproviders:\n  exa: {}\n  exa: {}\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  `duplicate provider "exa"`,
		},
		{
			name:     "unknown key inside a reserved block",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    auth: {mode: reject, bogus: 1}\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  "field bogus not found",
		},
		{
			name:     "turns is not a list",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns: {a: 1}\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  "expected a list of turns",
		},
		{
			name:     "unknown key inside a turn",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - respnd: {}\n",
			wantCode: "scenario.decode.failed",
			wantMsg:  "field respnd not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, report, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatal("expected an error")
			}
			var found bool
			for _, f := range report.Errors() {
				if f.Code == tc.wantCode && strings.Contains(f.Message, tc.wantMsg) {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s containing %q, got %+v", tc.wantCode, tc.wantMsg, report.Findings)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()

	_, report, err := Load(filepath.Join("testdata", "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(report.Errors()) != 1 || report.Errors()[0].Code != "scenario.unreadable" {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestLoadFS(t *testing.T) {
	t.Parallel()

	s, report, err := LoadFS(os.DirFS("testdata"), "plan-example.yaml")
	if err != nil {
		t.Fatalf("LoadFS: %v (%+v)", err, report.Findings)
	}
	if s.Path() != "plan-example.yaml" {
		t.Errorf("Path() = %q", s.Path())
	}

	if _, _, err := LoadFS(os.DirFS("testdata"), "nope.yaml"); err == nil {
		t.Error("a missing member must fail")
	}
}

func TestParse_DefaultsAreDeterministic(t *testing.T) {
	t.Parallel()

	s, _, err := Parse([]byte("version: 1\nname: n\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.BaseTime().Equal(DefaultBaseTime) {
		t.Errorf("BaseTime() = %v, want the pinned default", s.BaseTime())
	}
	if s.SeedKey() != "n" {
		t.Errorf("SeedKey() = %q, want the name", s.SeedKey())
	}

	pinned, _, err := Parse([]byte("version: 1\nname: n\nseed: s\ntime:\n  base: 2030-02-03T04:05:06Z\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if pinned.SeedKey() != "s" {
		t.Errorf("SeedKey() = %q", pinned.SeedKey())
	}
	if !pinned.BaseTime().Equal(time.Date(2030, time.February, 3, 4, 5, 6, 0, time.UTC)) {
		t.Errorf("BaseTime() = %v", pinned.BaseTime())
	}
}

func TestParse_IsByteIdenticalAcrossRuns(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("testdata", "turns.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for i := range 10 {
		s, _, err := Parse(src)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		var sb strings.Builder
		for _, name := range s.Providers.Names() {
			e := s.Providers.Get(name)
			sb.WriteString(name + "/" + e.Kind + ":")
			for j := range e.Turns {
				out, err := yaml.Marshal(&e.Turns[j].Respond)
				if err != nil {
					t.Fatal(err)
				}
				sb.Write(out)
			}
			sb.WriteString("\n")
		}
		if i == 0 {
			first = sb.String()
			continue
		}
		if sb.String() != first {
			t.Fatalf("run %d differs from run 0:\n%s\n---\n%s", i, first, sb.String())
		}
	}
}
