package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/internal/journal"
)

// defaultNamespace is the state lane a request with no /n/ prefix is served in.
// Entries recorded before namespaces were wired carry an empty Namespace, and
// filtering treats the two as the same lane.
const defaultNamespace = "default"

// RequestsResponse is the GET /__admin/requests body.
//
// Entries are in journal append order, oldest first, and each carries both
// ArrivedAt and CompletedAt. Those two instants are the evidence a fusion test
// rests on: a consumer proves its three provider calls overlapped rather than
// ran serially by showing one call arrived before another completed. They are
// real time by design (package-design §3.2) and are therefore the two fields a
// golden over this body must ignore.
type RequestsResponse struct {
	Entries []journal.Entry `json:"entries"`
	Stats   journal.Stats   `json:"stats"`
}

// filter is the parsed query of GET /__admin/requests.
type filter struct {
	// providers is the set of provider names to keep, empty for all. Matching is
	// case-insensitive because a provider name is a fixed lower-case token and an
	// upper-cased query parameter is a typo, not a different provider.
	providers []string

	// namespace is the state lane to keep, empty for all lanes.
	namespace string

	// since keeps entries whose Seq is strictly greater, so a poller can pass the
	// highest sequence it has already seen.
	since uint64

	// limit caps how many entries are returned, zero for no cap. It is applied
	// after every other filter and keeps the oldest matches, which is what makes
	// since plus limit a correct forward pager rather than a window that skips
	// entries whenever more than limit arrive between polls.
	limit int

	// indent turns on pretty-printing. It is off by default because
	// scripts/image-smoke.sh greps for an unindented literal.
	indent bool
}

// errInvalidSince and errInvalidLimit deliberately quote nothing from the
// request. An error body is a retained structure like any other, and a query
// parameter is one of the places a misconfigured adapter puts its credential
// (CLAUDE.md house rule 4), so no parameter value is ever echoed back.
var (
	errInvalidSince = errors.New("invalid since parameter: expected a journal sequence number")
	errInvalidLimit = errors.New("invalid limit parameter: expected a non-negative count")
)

// handleRequests serves the journal as JSON.
//
// Every entry is passed through journal.Redact on the way out. The journal
// already redacted at the storage boundary and Redact is idempotent, so this
// costs one pass on a cold path and closes the last route by which a Deps wired
// with some other journal.Journal implementation could serve a credential.
func (d Deps) handleRequests(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()}, false)
		return
	}

	stored := d.Journal.Snapshot()
	entries := make([]journal.Entry, 0, len(stored))
	for _, e := range stored {
		if !f.matches(e) {
			continue
		}
		entries = append(entries, journal.Redact(e))
		if f.limit > 0 && len(entries) == f.limit {
			break
		}
	}

	writeJSON(w, http.StatusOK, RequestsResponse{Entries: entries, Stats: d.Journal.Stats()}, f.indent)
}

// parseFilter reads the documented query parameters: provider, namespace,
// since, limit and pretty. An unrecognised parameter is ignored rather than
// rejected — the admin surface is a debugging tool, and failing a journal read
// because of a stray parameter helps nobody.
func parseFilter(q url.Values) (filter, error) {
	f := filter{indent: pretty(q)}

	for _, raw := range q["provider"] {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				f.providers = append(f.providers, name)
			}
		}
	}

	if ns := strings.TrimSpace(q.Get("namespace")); ns != "" {
		f.namespace = ns
	}

	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		since, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return filter{}, errInvalidSince
		}
		f.since = since
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return filter{}, errInvalidLimit
		}
		f.limit = limit
	}

	return f, nil
}

// pretty reports whether the response should be indented. Presence alone turns
// it on, so ?pretty and ?pretty=1 agree, and the two spellings of "off" a
// scripted caller is likely to interpolate are honoured.
func pretty(q url.Values) bool {
	if !q.Has("pretty") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(q.Get("pretty"))) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// matches reports whether e survives the filter.
func (f filter) matches(e journal.Entry) bool {
	if len(f.providers) > 0 && !containsFold(f.providers, e.Provider) {
		return false
	}
	if f.namespace != "" && !strings.EqualFold(namespaceOf(e), f.namespace) {
		return false
	}
	return e.Seq > f.since
}

// namespaceOf reports the lane an entry was served in, treating an unset
// Namespace as the default lane.
func namespaceOf(e journal.Entry) string {
	if e.Namespace == "" {
		return defaultNamespace
	}
	return e.Namespace
}

func containsFold(set []string, want string) bool {
	for _, s := range set {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
