package scenario

// PublishedAtLayout is the RFC 3339 layout every rendered timestamp uses.
// Millisecond precision matches Exa's own documented example; a date parser tuned
// to whole seconds would reject it, which is exactly the consumer bug worth
// surfacing in a test rather than in production.
const PublishedAtLayout = "2006-01-02T15:04:05.000Z07:00"

// RenderedSource is a provider-neutral view of a Source with defaults applied.
// Provider packages project this rather than reading Source directly, so
// defaulting rules live in one place.
type RenderedSource struct {
	ID          string
	URL         string
	Title       string
	Author      Nullable
	PublishedAt Nullable // RFC 3339, millisecond precision
	Text        string
	Snippets    []string
	Image       string
	Favicon     string
}

// Render applies defaults to a resolved reference and returns the neutral view.
// It reads ref.Target, which Resolve guarantees is non-nil; a zero RenderedSource
// with the ID set to ref.Ref is returned for the unresolved case so a render bug
// cannot become a nil dereference on a request path.
func Render(ref SourceRef) RenderedSource {
	if ref.Target == nil {
		return RenderedSource{ID: ref.Ref}
	}
	src := ref.Target
	out := RenderedSource{
		ID:       src.ID,
		URL:      src.URL,
		Title:    src.Title,
		Text:     src.Text,
		Snippets: src.Snippets,
		Image:    src.Image,
		Favicon:  src.Favicon,
	}
	switch {
	case src.AuthorNull:
		out.Author = NullNullable()
	case src.Author != "":
		out.Author = SetNullable(src.Author)
	}
	if src.PublishedAt != nil {
		out.PublishedAt = SetNullable(src.PublishedAt.UTC().Format(PublishedAtLayout))
	}
	return out
}

// FirstSnippet returns the first snippet, or the empty string. It is the default
// several providers use for a per-result excerpt, and having it here keeps the
// three provider packages from disagreeing about what "the snippet" means.
func (r RenderedSource) FirstSnippet() string {
	if len(r.Snippets) == 0 {
		return ""
	}
	return r.Snippets[0]
}
