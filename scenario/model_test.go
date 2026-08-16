package scenario

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestDecode_EveryProjectionStructRoundTrips is the guard against the inline-key
// collision class. An inlined struct's YAML keys share one namespace with its
// outer struct's, and yaml.v3 v3.0.1 does not return an error for a duplicate
// across that boundary — it *panics*, taking the process down on the first
// scenario that contains such an entry. The Go field names collide just as
// badly, silently, with no compiler or vet signal.
//
// The provider projection structs themselves now live in the provider packages,
// so this test covers every YAML-decodable struct this package declares and
// proves that CheckInlineKeys — which every projection reaches through
// DecodeStrict and Turn.DecodeProjection — actually rejects the collision.
func TestDecode_EveryProjectionStructRoundTrips(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		into func() any
	}{
		{"Scenario", "version: 1\nname: n\n", func() any { return &Scenario{} }},
		{"TimeConfig", "base: 2026-01-01T00:00:00Z\n", func() any { return &TimeConfig{} }},
		{"Source", "id: a\nurl: https://example.test/a\ntitle: A\nauthor: X\nauthor_null: true\npublished_at: 2026-05-20T00:00:00Z\ntext: t\nsnippets: [s]\nimage: i\nfavicon: f\nclaims:\n  - id: c\n    text: ct\n", func() any { return &Source{} }},
		{"Claim", "id: c\ntext: t\n", func() any { return &Claim{} }},
		{"SourceRef", "source: a\n", func() any { return &SourceRef{} }},
		{"Turn", "when:\n  call_index: 0\nrespond:\n  answer: hi\n", func() any { return &Turn{} }},
		{"Match", "call_index: 1\nbody_contains: x\nbody_json:\n  model: sonar\n", func() any { return &Match{} }},
		{"AuthPolicy", "mode: reject\nexpect_key: fake\nheaders: [authorization]\n", func() any { return &AuthPolicy{} }},
		{"ValidationPolicy", "strict: true\npromote: [a]\ndemote: [b]\n", func() any { return &ValidationPolicy{} }},
		{"Fault", "after: repeat_last\nattempts:\n  - status: 429\n", func() any { return &Fault{} }},
		{"FaultAttempt", "kind: truncate_body\nstatus: 200\ndelay: 250ms\nretry_after: 1\nheaders: {a: b}\nbody: {detail: {error: x}}\nerror: e\ntag: tg\nraw_body: '{'\ncontent_type: text/plain\ntruncate_after_bytes: 8\nreset: true\nextra_fields: {x: 1}\nrepeat: 3\n", func() any { return &FaultAttempt{} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := tc.into()
			if err := CheckInlineKeys(reflect.TypeOf(target)); err != nil {
				t.Fatalf("CheckInlineKeys: %v", err)
			}
			// yaml.Unmarshal is what panics on a duplicate inline key, so the
			// decode itself is half the assertion.
			if err := yaml.Unmarshal([]byte(tc.yaml), target); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			encoded, err := yaml.Marshal(target)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			again := tc.into()
			if err := yaml.Unmarshal(encoded, again); err != nil {
				t.Fatalf("re-unmarshal of %q: %v", encoded, err)
			}
		})
	}
}

func TestCheckInlineKeys_RejectsDuplicateInlineKey(t *testing.T) {
	t.Parallel()

	// The exact shape the design forbids: a result type that re-declares the
	// YAML key the inlined SourceRef already owns.
	type badResult struct {
		SourceRef `yaml:",inline"`
		Source    string `yaml:"source,omitempty"`
	}
	err := CheckInlineKeys(reflect.TypeOf(&badResult{}))
	if err == nil {
		t.Fatal("expected a duplicate-key error, got nil")
	}
	if !strings.Contains(err.Error(), `duplicated YAML key "source"`) {
		t.Fatalf("error does not name the duplicated key: %v", err)
	}

	// A shadowed Go field name is just as fatal and just as silent.
	type shadowing struct {
		SourceRef `yaml:",inline"`
		Ref       string `yaml:"ref_override,omitempty"`
	}
	err = CheckInlineKeys(reflect.TypeOf(&shadowing{}))
	if err == nil || !strings.Contains(err.Error(), `duplicated Go field name "Ref"`) {
		t.Fatalf("expected a shadowed-field error, got %v", err)
	}

	// The legitimate shape — a result type declaring its own ID beside the
	// inlined reference — must pass.
	type goodResult struct {
		SourceRef  `yaml:",inline"`
		ID         string `yaml:"id,omitempty"`
		SourceType string `yaml:"source_type,omitempty"`
	}
	if err := CheckInlineKeys(reflect.TypeOf(&goodResult{})); err != nil {
		t.Fatalf("legitimate inline shape rejected: %v", err)
	}
}

func TestSourceRef_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantRef string
		wantErr string
	}{
		{name: "scalar shorthand", yaml: "source-a", wantRef: "source-a"},
		{name: "mapping form", yaml: "{source: source-b}", wantRef: "source-b"},
		{name: "unknown sibling key", yaml: "{source: source-b, bogus: 1}", wantErr: "field bogus not found"},
		{name: "list", yaml: "[a]", wantErr: "expected a source id or a mapping"},
		// yaml.v3 short-circuits a null node before any custom unmarshaler runs,
		// so a null reference arrives as the zero value and is caught by Resolve
		// as an empty reference rather than here. See TestResolve_EmptyReference.
		{name: "null", yaml: "null", wantRef: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ref SourceRef
			err := yaml.Unmarshal([]byte(tc.yaml), &ref)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			case ref.Ref != tc.wantRef:
				t.Fatalf("Ref = %q, want %q", ref.Ref, tc.wantRef)
			}
		})
	}
}

func TestProviders_IterateInDeclarationOrder(t *testing.T) {
	t.Parallel()

	// Declaration order is deliberately not alphabetical and not insertion order
	// of any Go map, because a map range here is a test that passes locally and
	// flakes in CI.
	src := "version: 1\nname: n\nproviders:\n  zeta: {}\n  alpha: {}\n  perplexity: {}\n  exa: {}\n"
	want := []string{"zeta", "alpha", "perplexity", "exa"}

	for range 20 {
		s, _, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := s.Providers.Names(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

func TestProviderEntry_SingleShotNormalisesToOneTurn(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: n
sources:
  - {id: source-a, url: https://example.test/a, title: A}
providers:
  exa:
    kind: exa
    auth: {mode: reject}
    validation: {strict: true}
    fault:
      attempts:
        - status: 429
    results:
      - source: source-a
    cost_dollars: {total: 0.005}
    extra_fields: {x: 1}
`
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	e := s.Providers.Get("exa")
	if e == nil {
		t.Fatal("no exa entry")
	}
	if e.Kind != "exa" || e.Auth == nil || e.Auth.Mode != AuthReject || e.Validation == nil || !e.Validation.Strict {
		t.Fatalf("envelope not decoded: %+v", e)
	}
	if len(e.Turns) != 1 {
		t.Fatalf("want exactly one normalised turn, got %d", len(e.Turns))
	}
	turn := e.Turns[0]
	if turn.When != nil {
		t.Fatalf("normalised turn must be unconditional, got %+v", turn.When)
	}
	if !turn.Fault.HasAttempts() {
		t.Fatal("block-level fault did not move onto the normalised turn")
	}

	// The projection body is the block minus the reserved envelope keys, and
	// extra_fields deliberately stays in it.
	var body map[string]any
	if err := turn.Respond.Decode(&body); err != nil {
		t.Fatalf("decode respond: %v", err)
	}
	for _, reserved := range reservedEnvelopeKeys {
		if _, present := body[reserved]; present {
			t.Errorf("reserved key %q leaked into the projection body", reserved)
		}
	}
	for _, want := range []string{"results", "cost_dollars", "extra_fields"} {
		if _, present := body[want]; !present {
			t.Errorf("projection key %q missing from the body: %v", want, body)
		}
	}
	if !s.HasFaults() {
		t.Error("HasFaults() = false, want true")
	}
}

func TestProviderEntry_KindDefaultsToTheMapKey(t *testing.T) {
	t.Parallel()

	s, _, err := Parse([]byte("version: 1\nname: n\nproviders:\n  openai_fallback:\n    kind: openai\n  openai: {}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Providers.Get("openai").Kind; got != "openai" {
		t.Errorf("default Kind = %q, want the map key", got)
	}
	if got := s.Providers.Get("openai_fallback").Kind; got != "openai" {
		t.Errorf("explicit Kind = %q, want openai", got)
	}
	if s.Providers.Get("openai_fallback").Implemented {
		t.Error("Implemented must default to false; the composition layer sets it")
	}
}

func TestProviders_NilSafe(t *testing.T) {
	t.Parallel()

	var p *Providers
	if p.Names() != nil || p.Get("exa") != nil || p.Len() != 0 {
		t.Fatal("nil Providers must answer empty, not panic")
	}
	var s *Scenario
	if s.HasFaults() || s.SeedKey() != "servicesim" || !s.BaseTime().Equal(DefaultBaseTime) {
		t.Fatal("nil Scenario accessors must be safe")
	}
	if s.Provider("exa") != nil || s.Path() != "" {
		t.Fatal("nil Scenario must answer empty on every hop")
	}
	if _, ok := s.SourceByID("a"); ok || s.SourcesForClaim("c") != nil {
		t.Fatal("nil Scenario lookups must miss, not panic")
	}
	if err := s.Resolve(); err != nil {
		t.Fatalf("nil Scenario Resolve = %v", err)
	}
}

func TestTurn_DecodeProjection(t *testing.T) {
	t.Parallel()

	type exaProjection struct {
		Results []SourceRef `yaml:"results,omitempty"`
	}

	s, _, err := Parse([]byte("version: 1\nname: n\nsources:\n  - {id: source-a, url: https://example.test/a, title: A}\nproviders:\n  exa:\n    results: [source-a]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e := s.Providers.Get("exa")

	var proj exaProjection
	if err := e.Turns[0].DecodeProjection(e.Name, 0, &proj); err != nil {
		t.Fatalf("DecodeProjection: %v", err)
	}
	if len(proj.Results) != 1 || proj.Results[0].Ref != "source-a" {
		t.Fatalf("projection = %+v", proj)
	}

	// The error must name the provider and the turn index; "cannot unmarshal
	// string into int" with no location is unactionable in a twelve-turn fixture.
	type strictProjection struct {
		Count int `yaml:"count"`
	}
	bad, _, err := Parse([]byte("version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - {when: {call_index: 0}, respond: {count: 1}}\n      - respond: {count: not-a-number}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	entry := bad.Providers.Get("exa")
	var sp strictProjection
	err = entry.Turns[1].DecodeProjection(entry.Name, 1, &sp)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if !strings.Contains(err.Error(), "providers.exa.turns[1].respond") {
		t.Fatalf("error does not locate the turn: %v", err)
	}

	// Unknown keys inside a projection body are an authoring error, at every
	// level of nesting.
	var sp2 strictProjection
	if err := entry.Turns[0].DecodeProjection(entry.Name, 0, &sp2); err != nil {
		t.Fatalf("valid turn: %v", err)
	}
	type narrow struct {
		Other string `yaml:"other,omitempty"`
	}
	var n narrow
	if err := entry.Turns[0].DecodeProjection(entry.Name, 0, &n); err == nil {
		t.Fatal("expected an unknown-key error from the strict decode")
	}
}

func TestMatch_Matches(t *testing.T) {
	t.Parallel()

	idx := func(i int) *int { return &i }
	body := []byte(`{"model":"sonar","stream":false,"n":3,"messages":[{"role":"system"},{"role":"user"}]}`)

	// The route key serving the request under test, unless a case overrides it.
	const servingRoute = "exa:search"

	tests := []struct {
		name  string
		match *Match
		call  int
		route string
		want  bool
	}{
		{name: "nil matches everything", match: nil, call: 7, want: true},
		{name: "empty matches everything", match: &Match{}, call: 7, want: true},
		{name: "call index hit", match: &Match{CallIndex: idx(2)}, call: 2, want: true},
		{name: "call index miss", match: &Match{CallIndex: idx(2)}, call: 3, want: false},
		{name: "body contains hit", match: &Match{BodyContains: "sonar"}, want: true},
		{name: "body contains miss", match: &Match{BodyContains: "opus"}, want: false},
		{name: "body json scalar", match: &Match{BodyJSON: map[string]string{"model": "sonar"}}, want: true},
		{name: "body json bool", match: &Match{BodyJSON: map[string]string{"stream": "false"}}, want: true},
		{name: "body json number keeps its literal", match: &Match{BodyJSON: map[string]string{"n": "3"}}, want: true},
		{name: "body json array index", match: &Match{BodyJSON: map[string]string{"messages.1.role": "user"}}, want: true},
		{name: "body json missing path", match: &Match{BodyJSON: map[string]string{"nope": "x"}}, want: false},

		// Route is the axis a multi-route provider is unscriptable without.
		{name: "bare route hit", match: &Match{Route: "search"}, want: true},
		{name: "bare route miss", match: &Match{Route: "answer"}, want: false},
		{name: "qualified route hit", match: &Match{Route: "exa:search"}, want: true},
		{
			// A qualified name is never reduced to its own suffix first, or
			// pasting an Exa turn into a Tavily block would silently keep working
			// against the wrong provider's route.
			name:  "qualified route does not match another provider's same-named route",
			match: &Match{Route: "exa:search"},
			route: "tavily:search",
			want:  false,
		},
		{
			name:  "bare route does match across providers, which is the point of the bare form",
			match: &Match{Route: "search"},
			route: "tavily:search",
			want:  true,
		},
		{
			// The default TurnKey is ["route"], so this reads "the third call to
			// the poll route" — the shape every async surface needs.
			name:  "route and call index together",
			match: &Match{Route: "search", CallIndex: idx(2)},
			call:  2,
			want:  true,
		},
		{
			name:  "route matches but call index does not",
			match: &Match{Route: "search", CallIndex: idx(2)},
			call:  1,
			want:  false,
		},
		{
			name:  "an unqualified key with no colon still matches",
			match: &Match{Route: "bare"},
			route: "bare",
			want:  true,
		},
		{
			name:  "fields AND rather than OR",
			match: &Match{CallIndex: idx(0), BodyContains: "opus"},
			want:  false,
		},
		{
			name:  "every field satisfied",
			match: &Match{Route: "search", CallIndex: idx(0), BodyContains: "sonar", BodyJSON: map[string]string{"model": "sonar"}},
			want:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			route := tc.route
			if route == "" {
				route = servingRoute
			}
			if got := tc.match.Matches(tc.call, route, body); got != tc.want {
				t.Fatalf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatch_IsEmptyCountsRoute(t *testing.T) {
	t.Parallel()

	// Route must participate in IsEmpty, or a turn constrained ONLY by route
	// would be treated as the unconditional fallback — scenario.Validate uses
	// IsEmpty to find that fallback, so a route-only turn would be mistaken for
	// one and every later turn reported as unreachable.
	if (&Match{Route: "search"}).IsEmpty() {
		t.Error("a route-only predicate constrains something")
	}
	if !(&Match{}).IsEmpty() {
		t.Error("an all-zero predicate is empty")
	}
}

func TestRouteMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		authored string
		key      string
		want     bool
	}{
		{"search", "exa:search", true},
		{"exa:search", "exa:search", true},
		{"answer", "exa:search", false},
		{"exa:search", "tavily:search", false},
		{"exa:answer", "exa:search", false},
		{"completions", "perplexity:completions", true},
		{"bare", "bare", true},
		{"bare", "other", false},
		{"", "exa:search", false},
	}
	for _, tc := range tests {
		t.Run(tc.authored+"/"+tc.key, func(t *testing.T) {
			t.Parallel()
			if got := RouteMatches(tc.authored, tc.key); got != tc.want {
				t.Errorf("RouteMatches(%q, %q) = %v, want %v", tc.authored, tc.key, got, tc.want)
			}
		})
	}
}

func TestRouteKeySuffix(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"exa:search":             "search",
		"perplexity:completions": "completions",
		"bare":                   "bare",
		"a:b:c":                  "c",
		"":                       "",
	}
	for key, want := range tests {
		if got := RouteKeySuffix(key); got != want {
			t.Errorf("RouteKeySuffix(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestMatch_MatchesIsDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()

	m := &Match{BodyJSON: map[string]string{"model": "sonar", "messages.0.role": "system"}}
	body := []byte(`{"model":"sonar","messages":[{"role":"system"}]}`)
	for range 50 {
		if !m.Matches(0, "perplexity:completions", body) {
			t.Fatal("map iteration order reached the matching decision")
		}
	}
}

func TestTurnKey_Extractors(t *testing.T) {
	t.Parallel()

	if got := (TurnKey(nil)).Extractors(); !reflect.DeepEqual(got, []string{TurnKeyRoute}) {
		t.Fatalf("default = %v", got)
	}
	k := TurnKey{"route", "body_json:model"}
	if got := k.Extractors(); !reflect.DeepEqual(got, []string(k)) {
		t.Fatalf("explicit = %v", got)
	}
}

func TestNullable(t *testing.T) {
	t.Parallel()

	var doc struct {
		Absent  Nullable `yaml:"absent,omitempty"`
		Null    Nullable `yaml:"null_value"`
		Present Nullable `yaml:"present"`
	}
	if err := yaml.Unmarshal([]byte("null_value: null\npresent: hello\n"), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !doc.Absent.IsZero() {
		t.Error("absent must be the zero value")
	}
	// yaml.v3 never routes a null node through a custom unmarshaler, so an
	// explicit null decodes to the absent state. Both render as JSON null, which
	// is what the wire contract cares about; see the UnmarshalYAML comment.
	if !doc.Null.IsZero() {
		t.Error("an explicit YAML null decodes to the absent state")
	}
	if !NullNullable().IsNull() || NullNullable().IsZero() {
		t.Error("the explicit-null state must be constructible in Go")
	}
	if v, ok := doc.Present.Get(); !ok || v != "hello" {
		t.Errorf("Get() = %q, %v", v, ok)
	}

	for _, tc := range []struct {
		name string
		in   Nullable
		want string
	}{
		{"absent", Nullable{}, "null"},
		{"null", NullNullable(), "null"},
		{"value", SetNullable("x"), `"x"`},
	} {
		got, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: MarshalJSON = %s, want %s", tc.name, got, tc.want)
		}
	}

	var bad struct {
		V Nullable `yaml:"v"`
	}
	if err := yaml.Unmarshal([]byte("v: [1]\n"), &bad); err == nil {
		t.Error("a list must not decode into a Nullable")
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()

	var doc struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal([]byte("d: 250ms\n"), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.D.Duration() != 250*time.Millisecond {
		t.Fatalf("Duration() = %v", doc.D.Duration())
	}
	got, err := json.Marshal(doc.D)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != "250" {
		t.Fatalf("MarshalJSON = %s, want milliseconds", got)
	}
	if err := yaml.Unmarshal([]byte("d: not-a-duration\n"), &doc); err == nil {
		t.Error("an unparseable duration must fail loudly")
	}
}

func TestFaultAttempt_EffectiveKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   FaultAttempt
		want FaultKind
	}{
		{"empty", FaultAttempt{}, FaultNone},
		{"trailing success", FaultAttempt{Status: 200}, FaultNone},
		{"status inferred", FaultAttempt{Status: 429}, FaultStatus},
		{"explicit wins", FaultAttempt{Kind: FaultEmptyBody, Status: 500}, FaultEmptyBody},
		{"raw body", FaultAttempt{RawBody: "{"}, FaultInvalidJSON},
		{"content type", FaultAttempt{ContentType: "text/plain"}, FaultWrongContentType},
		{"truncation", FaultAttempt{TruncateAfterBytes: 8}, FaultTruncateBody},
		{"reset", FaultAttempt{Reset: true}, FaultTruncateBody},
		{"body_bytes inferred", FaultAttempt{BodyBytes: 100}, FaultOversizedBody},
		{"body_bytes composes with a status override", FaultAttempt{BodyBytes: 100, Status: 500}, FaultOversizedBody},
		{"explicit oversized_body wins over status", FaultAttempt{Kind: FaultOversizedBody, Status: 500}, FaultOversizedBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.EffectiveKind(); got != tc.want {
				t.Fatalf("EffectiveKind = %q, want %q", got, tc.want)
			}
		})
	}

	if (FaultAttempt{}).Repeats() != 1 || (FaultAttempt{Repeat: 3}).Repeats() != 3 {
		t.Fatal("Repeats must treat zero and one as equivalent")
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()

	s := Empty()
	if report := s.Validate(); !report.OK() {
		t.Fatalf("Empty() must be valid, got %+v", report.Findings)
	}
	if s.Providers.Len() != 0 || len(s.Sources) != 0 {
		t.Fatal("Empty() must have no sources and no projections")
	}
	if _, ok := s.SourceByID("anything"); ok {
		t.Fatal("Empty() must resolve nothing")
	}
}
