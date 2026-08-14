package admin

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
)

// defaultNamespace is the state lane a request with no /n/ prefix is served in.
// Entries recorded before namespaces were wired carry an empty Namespace, and
// this surface reports both as the same lane.
//
// It takes the provider seam's spelling because that is the package which named
// the lane the entry was served in. journal declares the same constant, through
// a package that must not import the seam; the two agreeing is asserted by a
// test rather than assumed here.
const defaultNamespace = provider.DefaultNamespace

// RequestsResponse is the GET /__admin/requests body.
//
// Entries are in journal append order, oldest first, and each carries both
// ArrivedAt and CompletedAt. Those two instants are the evidence a fusion test
// rests on: a consumer proves its three provider calls overlapped rather than
// ran serially by showing one call arrived before another completed. They are
// real time by design (package-design §3.2) and are therefore the two fields a
// golden over this body must ignore.
//
// Every entry also names the namespace it was served in, including the entries
// served in the default lane. On a shared container that field is what tells a
// reader which of the concurrent tests a request belonged to, so it is never
// left for them to infer.
type RequestsResponse struct {
	Entries []journal.Entry `json:"entries"`
	Stats   journal.Stats   `json:"stats"`
}

// NamespacesResponse is the GET /__admin/namespaces body. Namespaces are sorted
// by name, because Go's map iteration order must never reach output.
type NamespacesResponse struct {
	Namespaces []NamespaceSummary `json:"namespaces"`
}

// NamespaceSummary is one live state lane and how much journal it holds.
//
// It exists for the developer debugging a shared container, whose first question
// is "which lanes are alive in there, and did my requests land in mine?". Before
// this endpoint the only way to answer it was to read every entry.
type NamespaceSummary struct {
	// Namespace is the lane's name, "default" for traffic that carried no /n/
	// prefix.
	Namespace string `json:"namespace"`

	// Entries is how many journal entries the lane currently retains. It is what
	// the lane holds now, not what it was ever sent: a lane at capacity has
	// evicted its oldest, and the retention counters on GET /__admin/requests
	// report that.
	Entries int `json:"entries"`
}

// namespaceLister is the optional capability a journal declares when it can
// enumerate its live lanes, including a lane that holds no entries — one whose
// requests were all evicted, or whose sequence numbers were drawn before its
// first entry completed. A journal without it is enumerated from its entries
// instead, which finds every lane that has something to show.
type namespaceLister interface {
	Namespaces() []string
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

// errInvalidSince, errInvalidLimit and errInvalidNamespace deliberately quote
// nothing from the request. An error body is a retained structure like any
// other, and a query parameter is one of the places a misconfigured adapter puts
// its credential (CLAUDE.md house rule 4), so no parameter value is ever echoed
// back.
var (
	errInvalidSince     = errors.New("invalid since parameter: expected a journal sequence number")
	errInvalidLimit     = errors.New("invalid limit parameter: expected a non-negative count")
	errInvalidNamespace = fmt.Errorf(
		"invalid namespace parameter: expected 1 to %d characters from [A-Za-z0-9_-]",
		provider.MaxNamespaceNameLen)
)

// handleRequests serves the journal as JSON.
//
// ?namespace=<name> scopes the read to one state lane; without it every lane is
// returned, oldest first, with each entry naming the lane it belongs to. Stats
// describe what the journal retains overall either way — they answer "did
// something get evicted", which is not a question about the filtered page. Per
// lane counts are what GET /__admin/namespaces is for.
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

	stored := d.snapshot(f.namespace)
	entries := make([]journal.Entry, 0, len(stored))
	for _, e := range stored {
		if !f.matches(e) {
			continue
		}
		e = journal.Redact(e)
		// Name the lane on the way out, without rewriting it in storage: the
		// journal keeps an entry's namespace exactly as it was appended, and an
		// entry that predates namespaces still belongs to the default lane. The
		// served representation says which lane that is, so a reader of a shared
		// container's journal never has to infer it from an absent field.
		e.Namespace = namespaceOf(e)
		entries = append(entries, e)
		if f.limit > 0 && len(entries) == f.limit {
			break
		}
	}

	writeJSON(w, http.StatusOK, RequestsResponse{Entries: entries, Stats: d.Journal.Stats()}, f.indent)
}

// snapshot returns the entries a read starts from: one namespace's when the
// filter names one, every namespace's otherwise.
//
// The scoped read goes through journal.SnapshotIn rather than filtering
// afterwards, so a journal that isolates lanes answers from the lane itself.
// That is the authoritative answer — the lane an entry was stored in is what the
// journal indexed it by — and it is also the cheap one, because a shared
// container's other thousand lanes are never copied.
func (d Deps) snapshot(namespace string) []journal.Entry {
	if namespace == "" {
		return d.Journal.Snapshot()
	}
	return journal.SnapshotIn(d.Journal, namespace)
}

// handleNamespaces lists the live state lanes with the number of journal entries
// each currently holds.
func (d Deps) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, NamespacesResponse{Namespaces: d.namespaces()}, pretty(r.URL.Query()))
}

// namespaces summarises every live lane, sorted by name.
//
// A journal that can enumerate its lanes is asked directly, which is what makes
// a lane that holds no entries visible: it exists, it counts against
// --max-namespaces, and a developer wondering why a new namespace was refused
// needs to see it. Anything else is enumerated from the entries themselves.
func (d Deps) namespaces() []NamespaceSummary {
	lister, canList := d.Journal.(namespaceLister)
	scoped, isScoped := d.Journal.(journal.Namespaced)
	if canList && isScoped {
		names := lister.Namespaces()
		out := make([]NamespaceSummary, 0, len(names))
		for _, name := range names {
			out = append(out, NamespaceSummary{Namespace: name, Entries: scoped.StatsIn(name).Stored})
		}
		// Sorted here as well as by the lister: this body is a candidate for a
		// golden, and "the implementation happened to be sorted" is not a property
		// this endpoint should depend on.
		slices.SortFunc(out, func(a, b NamespaceSummary) int { return strings.Compare(a.Namespace, b.Namespace) })
		return out
	}

	counts := make(map[string]int)
	for _, e := range d.Journal.Snapshot() {
		counts[namespaceOf(e)]++
	}
	out := make([]NamespaceSummary, 0, len(counts))
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, NamespaceSummary{Namespace: name, Entries: counts[name]})
	}
	return out
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

	namespace, err := namespaceParam(q)
	if err != nil {
		return filter{}, err
	}
	f.namespace = namespace

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

// namespaceParam reads the ?namespace= parameter, returning "" when it is
// absent or empty and an error when it is present but not a name the simulator
// could ever have created.
//
// It is validated rather than merely matched because both callers are better off
// for it: a read filter that silently returned nothing for a misspelt name sends
// the reader looking for missing requests, and a reset must never create or
// address state under a name nothing checked. The grammar is the seam's, so the
// names accepted here are exactly the names an /n/ prefix can produce.
//
// Matching is case-sensitive, unlike the provider filter. A namespace is a map
// key chosen by the caller, and "t-1" and "T-1" are two lanes; folding them
// would report one test's requests to another, which is the failure the whole
// namespace mechanism exists to prevent.
func namespaceParam(q url.Values) (string, error) {
	namespace := strings.TrimSpace(q.Get("namespace"))
	if namespace == "" {
		return "", nil
	}
	if !provider.ValidNamespace(namespace) {
		return "", errInvalidNamespace
	}
	return namespace, nil
}

// pretty reports whether the response should be indented. It is off by default
// because scripts/image-smoke.sh greps for an unindented literal.
func pretty(q url.Values) bool {
	return boolParam(q, "pretty")
}

// boolParam reads a presence-style boolean query parameter. Presence alone turns
// it on, so ?all and ?all=true agree, and the spellings of "off" a scripted
// caller is likely to interpolate are honoured rather than read as "on".
func boolParam(q url.Values, name string) bool {
	if !q.Has(name) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(q.Get(name))) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// matches reports whether e survives the filter.
//
// The namespace is deliberately absent: a scoped read is answered from the lane
// itself (see [Deps.snapshot]), so re-checking the field here would drop an
// entry whose lane is right and whose field a caller's own journal
// implementation left unset.
func (f filter) matches(e journal.Entry) bool {
	if len(f.providers) > 0 && !containsFold(f.providers, e.Provider) {
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
