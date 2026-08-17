package mcp

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/scenario"
)

// Projection is the whole server state one turn renders: server/discover's
// instructions and caching hints, tools/list's catalogue, and tools/call's
// results, all served from the same projection because a turn IS a server
// state (contracts/mcp/README.md's own framing) — tool-schema drift
// between calls is expressed as turn N vs turn N+1, not as three separate
// per-method projections that could disagree with each other about what
// the server currently believes its own catalogue is.
//
// It lives here rather than in the scenario package for the same reason
// every other provider's Projection does: scenario is level 1 and cannot
// know what an MCP tool result looks like without importing this package
// and closing an import cycle.
type Projection struct {
	// Instructions is server/discover's optional natural-language field.
	Instructions string `yaml:"instructions,omitempty"`

	// TTLMs overrides DefaultTTLMs for server/discover and tools/list.
	// A pointer so a scenario can request the documented "0 means
	// immediately stale" value, which the Go zero value cannot represent
	// alongside "unset, use the default".
	TTLMs *int64 `yaml:"ttl_ms,omitempty"`

	// CacheScope overrides DefaultCacheScope ("public" or "private").
	CacheScope string `yaml:"cache_scope,omitempty"`

	// Tools is tools/list's catalogue, in declaration order — the order
	// this profile always answers in (decision 8).
	Tools []ToolProjection `yaml:"tools,omitempty"`

	// Results is tools/call's scripted outcomes, keyed by tool name. A
	// key naming no declared Tools entry is legal — a hidden tool — and
	// a Tools entry with no matching Results key renders the "unscripted"
	// shape (tools.go).
	Results map[string]ResultProjection `yaml:"results,omitempty"`

	// Stream is the tools/call streaming script (decision 5). It is
	// meaningless for server/discover and tools/list, which never stream
	// regardless of policy.
	Stream scenario.StreamScript `yaml:"stream,omitempty"`

	// ExtraFields are merged into every result body this projection
	// renders — server/discover, tools/list and tools/call alike — which
	// is a broader reading than "results:" alone suggests: extra_fields
	// proves additive-field tolerance, and a consumer's tolerance is
	// worth proving on every method this profile answers, not only the
	// one whose block happens to sit next to this key in the YAML.
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ToolProjection is one tools/list entry.
type ToolProjection struct {
	Name         string           `yaml:"name"`
	Title        string           `yaml:"title,omitempty"`
	Description  string           `yaml:"description,omitempty"`
	InputSchema  map[string]any   `yaml:"input_schema"`
	OutputSchema map[string]any   `yaml:"output_schema,omitempty"`
	Annotations  *ToolAnnotations `yaml:"annotations,omitempty"`
}

// ToolAnnotations is the schema's ToolAnnotations: four hints plus a
// display title, all optional. Pointers, not bools, for the three-value
// hints (unset / true / false) — the schema documents defaults for an
// ABSENT hint (readOnlyHint false, destructiveHint true, and so on) that
// only matter to a client, so this profile never fills them in; it emits
// exactly what the fixture declared and nothing else.
type ToolAnnotations struct {
	Title           string `yaml:"title,omitempty"`
	ReadOnlyHint    *bool  `yaml:"read_only_hint,omitempty"`
	DestructiveHint *bool  `yaml:"destructive_hint,omitempty"`
	IdempotentHint  *bool  `yaml:"idempotent_hint,omitempty"`
	OpenWorldHint   *bool  `yaml:"open_world_hint,omitempty"`
}

// ResultProjection is one tools/call scripted outcome.
type ResultProjection struct {
	Content           []ContentBlock `yaml:"content,omitempty"`
	StructuredContent any            `yaml:"structured_content,omitempty"`
	IsError           bool           `yaml:"is_error,omitempty"`
}

// ContentBlock is one entry of a CallToolResult's content array. Type
// selects which of the schema's five ContentBlock members this renders as;
// see [renderContentBlock].
type ContentBlock struct {
	Type string `yaml:"type"`

	// Text is the literal text for type: text. Source, below, is the
	// alternative: a source-ref that resolves to a source's own text.
	Text string `yaml:"text,omitempty"`

	// Source is a scenario source id, for type: text only. See this
	// package's doc comment ("Content source references") for exactly
	// how it resolves and why it is not scenario.SourceRef.
	Source string `yaml:"source,omitempty"`

	// Data and MimeType serve type: image and type: audio.
	Data     string `yaml:"data,omitempty"`
	MimeType string `yaml:"mime_type,omitempty"`

	// URI, Name, Title and Description serve type: resource_link.
	// MimeType above doubles for its mimeType too.
	URI         string `yaml:"uri,omitempty"`
	Name        string `yaml:"name,omitempty"`
	Title       string `yaml:"title,omitempty"`
	Description string `yaml:"description,omitempty"`

	// Resource serves type: resource.
	Resource *ResourceBlock `yaml:"resource,omitempty"`

	// resolvedText is filled in by resolveContentSources when Source is
	// set; Text stays as authored (empty) so a Validator finding can
	// still say which key was empty in the fixture.
	resolvedText string
}

// ResourceBlock is the embedded resource of a type: resource content
// block — TextResourceContents (text set) or BlobResourceContents (blob
// set); this profile does not reject a fixture that sets both, since
// nothing downstream would be confused by it.
type ResourceBlock struct {
	URI      string `yaml:"uri"`
	MimeType string `yaml:"mime_type,omitempty"`
	Text     string `yaml:"text,omitempty"`
	Blob     string `yaml:"blob,omitempty"`
}

// contentTypes is the schema's ContentBlock discriminator values.
var contentTypes = []string{"text", "image", "audio", "resource_link", "resource"}

// sourceText returns the text a content block's source: reference
// resolves to: the source's own Text when it has one — where a
// malicious-content marker lives — falling back to its Title so a
// source authored title-only still renders something.
func sourceText(src *scenario.Source) string {
	if src.Text != "" {
		return src.Text
	}
	return src.Title
}

// resolveContentSources resolves every content block's source: reference
// reachable from p.Results, mutating each ContentBlock's resolvedText in
// place, and returns one ERROR finding per reference naming no declared
// source. Results are walked in sorted key order — never Go map order —
// so findings come back in a stable, diffable sequence.
//
// It is not threaded through scenario.ResolveRefs; see this package's doc
// comment for why.
func resolveContentSources(s *scenario.Scenario, path string, p *Projection) []scenario.Finding {
	var findings []scenario.Finding
	for _, name := range resultKeys(p.Results) {
		rp := p.Results[name]
		for i := range rp.Content {
			cb := &rp.Content[i]
			if cb.Type != "text" || cb.Source == "" {
				continue
			}
			src, ok := s.SourceByID(cb.Source)
			if !ok {
				findings = append(findings, scenario.Finding{
					Severity: scenario.SeverityError,
					Code:     CodeSourceUnknown,
					Path:     fmt.Sprintf("%s.results.%s.content[%d].source", path, name, i),
					Message:  fmt.Sprintf("unknown source %q", cb.Source),
				})
				continue
			}
			cb.resolvedText = sourceText(src)
		}
	}
	return findings
}

// resultKeys returns m's keys sorted, so every walk over p.Results is
// deterministic.
func resultKeys(m map[string]ResultProjection) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ttlMs returns p's ttl_ms override, or DefaultTTLMs.
func ttlMs(p *Projection) int64 {
	if p.TTLMs != nil {
		return *p.TTLMs
	}
	return DefaultTTLMs
}

// cacheScopeOf returns p's cache_scope override, or DefaultCacheScope.
func cacheScopeOf(p *Projection) string {
	if p.CacheScope != "" {
		return p.CacheScope
	}
	return DefaultCacheScope
}

// -----------------------------------------------------------------------
// Wire shapes and rendering
// -----------------------------------------------------------------------

// capabilities is ServerCapabilities, restricted to what this profile ever
// declares: {"tools":{}} (decision 9 — no listChanged, since this profile
// emits no list-change notifications).
type capabilities struct {
	Tools struct{} `json:"tools"`
}

// discoverResult is DiscoverResult.
type discoverResult struct {
	ResultType        string       `json:"resultType"`
	SupportedVersions []string     `json:"supportedVersions"`
	Capabilities      capabilities `json:"capabilities"`
	Instructions      string       `json:"instructions,omitempty"`
	TTLMs             int64        `json:"ttlMs"`
	CacheScope        string       `json:"cacheScope"`
	Meta              *result      `json:"_meta,omitempty"`
}

// renderDiscover renders server/discover's result body (not yet wrapped in
// the JSON-RPC envelope).
func renderDiscover(p *Projection) ([]byte, error) {
	body := discoverResult{
		ResultType:        resultType,
		SupportedVersions: supportedVersions,
		Instructions:      p.Instructions,
		TTLMs:             ttlMs(p),
		CacheScope:        cacheScopeOf(p),
		Meta:              serverInfoMeta(),
	}
	return wire.Render(body, p.ExtraFields)
}

// wireTool is Tool.
type wireTool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description,omitempty"`
	InputSchema  map[string]any   `json:"inputSchema"`
	OutputSchema map[string]any   `json:"outputSchema,omitempty"`
	Annotations  *wireAnnotations `json:"annotations,omitempty"`
}

// wireAnnotations is ToolAnnotations.
type wireAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// renderTool projects a fixture tool onto the wire shape. A tool declared
// with no input_schema at all renders the schema's own no-parameter
// example, {"type":"object"}, rather than an absent (and therefore
// schema-invalid) key.
func renderTool(t ToolProjection) wireTool {
	wt := wireTool{
		Name: t.Name, Title: t.Title, Description: t.Description,
		InputSchema: t.InputSchema, OutputSchema: t.OutputSchema,
	}
	if wt.InputSchema == nil {
		wt.InputSchema = map[string]any{"type": "object"}
	}
	if t.Annotations != nil {
		wt.Annotations = &wireAnnotations{
			Title:           t.Annotations.Title,
			ReadOnlyHint:    t.Annotations.ReadOnlyHint,
			DestructiveHint: t.Annotations.DestructiveHint,
			IdempotentHint:  t.Annotations.IdempotentHint,
			OpenWorldHint:   t.Annotations.OpenWorldHint,
		}
	}
	return wt
}

// listToolsResult is ListToolsResult. NextCursor is never set: this
// profile always serves a single page (decision 8).
type listToolsResult struct {
	ResultType string     `json:"resultType"`
	Tools      []wireTool `json:"tools"`
	TTLMs      int64      `json:"ttlMs"`
	CacheScope string     `json:"cacheScope"`
	Meta       *result    `json:"_meta,omitempty"`
}

// renderToolsList renders tools/list's result body.
func renderToolsList(p *Projection) ([]byte, error) {
	tools := make([]wireTool, 0, len(p.Tools))
	for _, t := range p.Tools {
		tools = append(tools, renderTool(t))
	}
	body := listToolsResult{
		ResultType: resultType,
		Tools:      tools,
		TTLMs:      ttlMs(p),
		CacheScope: cacheScopeOf(p),
		Meta:       serverInfoMeta(),
	}
	return wire.Render(body, p.ExtraFields)
}

// wireContent is ContentBlock. One struct for all five wire shapes: their
// field sets do not collide in a way that would make a merged struct
// ambiguous. It does not marshal directly — MarshalJSON below renders
// exactly the schema-required key set per Type, because a blanket
// omitempty across every field (the naive shared-struct approach) would
// drop a required key whenever a fixture author left it its Go zero
// value: an empty text: "" content block would render as {"type":"text"}
// with no "text" key at all, and a resource_link with no name: would
// render with no "name" key — both schema-invalid, since the schema marks
// those keys required by presence, not by non-emptiness.
type wireContent struct {
	Type        string
	Text        string
	Data        string
	MimeType    string
	URI         string
	Name        string
	Title       string
	Description string
	Resource    *wireResourceContents
}

// MarshalJSON renders c per its Type, including every field the schema
// marks required for that type even when it is the empty string — only
// Type itself decides the field set; the fixture author is trusted to
// have set the right ones (renderContentBlock is the only caller, and
// Validator's checkProjection rejects an unrecognised Type before this
// ever runs).
func (c wireContent) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case "text":
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{c.Type, c.Text})
	case "image", "audio":
		return json.Marshal(struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}{c.Type, c.Data, c.MimeType})
	case "resource_link":
		return json.Marshal(struct {
			Type        string `json:"type"`
			URI         string `json:"uri"`
			Name        string `json:"name"`
			Title       string `json:"title,omitempty"`
			Description string `json:"description,omitempty"`
			MimeType    string `json:"mimeType,omitempty"`
		}{c.Type, c.URI, c.Name, c.Title, c.Description, c.MimeType})
	case "resource":
		return json.Marshal(struct {
			Type     string                `json:"type"`
			Resource *wireResourceContents `json:"resource,omitempty"`
		}{c.Type, c.Resource})
	default:
		// Unreachable through a validated scenario; see renderContentBlock's
		// own fallback for why this still renders something rather than
		// panicking for a Projection built by hand past validation.
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		}{c.Type, c.Text})
	}
}

// wireResourceContents is TextResourceContents/BlobResourceContents,
// merged: exactly one of Text/Blob is set by a well-formed fixture.
type wireResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// renderContentBlock projects one fixture content block onto its wire
// shape by type.
func renderContentBlock(cb ContentBlock) wireContent {
	switch cb.Type {
	case "text":
		text := cb.Text
		if cb.Source != "" {
			text = cb.resolvedText
		}
		return wireContent{Type: "text", Text: text}
	case "image", "audio":
		return wireContent{Type: cb.Type, Data: cb.Data, MimeType: cb.MimeType}
	case "resource_link":
		return wireContent{
			Type: "resource_link", URI: cb.URI, Name: cb.Name,
			Title: cb.Title, Description: cb.Description, MimeType: cb.MimeType,
		}
	case "resource":
		var res *wireResourceContents
		if cb.Resource != nil {
			res = &wireResourceContents{
				URI: cb.Resource.URI, MimeType: cb.Resource.MimeType,
				Text: cb.Resource.Text, Blob: cb.Resource.Blob,
			}
		}
		return wireContent{Type: "resource", Resource: res}
	default:
		// Unreachable through a validated scenario (Validator rejects an
		// unrecognised type at load); a best-effort pass-through beats a
		// panic for a Projection built by hand past validation.
		return wireContent{Type: cb.Type, Text: cb.Text}
	}
}

// callToolResult is CallToolResult.
type callToolResult struct {
	ResultType        string        `json:"resultType"`
	Content           []wireContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
	Meta              *result       `json:"_meta,omitempty"`
}

// renderCallResult renders one tools/call result body from a scripted (or
// synthesised — unknown/unscripted tool) ResultProjection. extra is the
// projection-level extra_fields, merged into every result this profile
// renders.
func renderCallResult(rp *ResultProjection, extra scenario.ExtraFields) ([]byte, error) {
	content := make([]wireContent, 0, len(rp.Content))
	for _, cb := range rp.Content {
		content = append(content, renderContentBlock(cb))
	}
	body := callToolResult{
		ResultType:        resultType,
		Content:           content,
		StructuredContent: rp.StructuredContent,
		IsError:           rp.IsError,
		Meta:              serverInfoMeta(),
	}
	return wire.Render(body, extra)
}
