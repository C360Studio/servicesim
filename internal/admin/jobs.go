package admin

import (
	"net/http"
	"time"

	"github.com/c360studio/servicesim/internal/jobs"
)

// JobSummary is one async job record as GET /__admin/jobs reports it.
//
// It is a dedicated type rather than [jobs.Job] itself, so a field added to
// the internal record does not leak onto the wire by default — the same
// reasoning that keeps journal.Entry and the admin surface's own response
// types separate from their storage layer.
//
// Two fields a reader might expect are deliberately absent:
//
//   - A turn index (the design's §7.4 once listed one) is not served because
//     the record does not hold it and nothing can read it: the poll cursor
//     lives in the fault engine's attempt counter, and that seam offers no
//     non-claiming read (docs/design/async-jobs.md §8, "localCursor is not
//     readable"). Exposing a stale copy here would invite exactly the
//     derivation this listing must not attempt.
//   - LaneKey, which the record does hold, is not served, and this is a
//     house-rule-4 decision rather than an oversight. A lane key embeds
//     whatever a route's turn_key or
//     Route.LaneFrom extractors resolved verbatim — including a
//     "header:authorization" or "body_json:api_key" value a scenario author
//     wrote, which scenario validation does not refuse and nothing redacts.
//     A structure served over HTTP is the wrong place to find that out, so
//     the listing carries no lane key at all.
type JobSummary struct {
	// ID is the identifier the create returned and a poll presents.
	ID string `json:"id"`

	// Namespace is the state lane this record belongs to, "default" for a job
	// created with no /n/ prefix.
	Namespace string `json:"namespace"`

	// Entry is the scenario provider entry that minted this job, for example
	// "exa_agent_runs".
	Entry string `json:"entry"`

	// CreateIndex is the call index the create claimed.
	CreateIndex int `json:"create_index"`

	// CreatedAt is real time, stamped by the injectable clock at create time.
	// It is served here for diagnostics — same rule as journal ArrivedAt — and
	// is NEVER rendered into a provider response body: a response that carried
	// it would stop being byte-identical between runs.
	CreatedAt time.Time `json:"created_at"`
}

// JobsResponse is the GET /__admin/jobs body.
//
// Jobs is in a declared total order: namespace ascending, then entry, then
// create index, then id. Every field compared is a string or an int, and
// (namespace, id) is unique, so the order is total and pinned by a test — Go's
// map iteration must never reach this response (CLAUDE.md house rule 2).
type JobsResponse struct {
	Jobs []JobSummary `json:"jobs"`

	// Bound is the process's per-namespace job limit — --max-jobs, or its
	// default when unset. It is the same number for every namespace, because
	// the bound is a process-wide configuration value applied per lane, not a
	// per-namespace quantity.
	Bound int `json:"bound"`
}

// jobLister is the optional capability a job store declares when it can
// enumerate its live records, in the order [JobsResponse] documents.
//
// It is asserted for rather than added to [jobs.Store] because that interface
// is exported and consumers implement it (CLAUDE.md house rule 7): adding a
// method there would break every implementation outside this repository. A
// store without this capability makes GET /__admin/jobs impossible to honour,
// and the handler says so with a 501 rather than reporting an empty list that
// looks like "no jobs" instead of "cannot answer".
type jobLister interface {
	List() []jobs.Job
}

// errJobsNotListable is returned when the wired job store cannot enumerate its
// records. It quotes nothing from the request, matching the other reset-scope
// errors' rule (CLAUDE.md house rule 4).
const errJobsNotListable = "the wired job store cannot enumerate its records"

// handleJobs serves the async job registry as JSON: a read-only view, so
// nothing here mutates state (CLAUDE.md house rule 6).
//
// ?namespace=<name> scopes the listing to one state lane. Absent or blank,
// every namespace's jobs are returned — the same "no filter" behaviour
// /__admin/requests uses, not the "default" a poll's Lookup normalises an
// empty namespace to. A job created with no /n/ prefix lives in "default", so
// ?namespace=default finds it like any other named lane.
func (d Deps) handleJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	namespace, err := namespaceParam(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()}, false)
		return
	}

	lister, ok := d.Jobs.(jobLister)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errJobsNotListable}, false)
		return
	}

	all := lister.List()
	summaries := make([]JobSummary, 0, len(all))
	for _, j := range all {
		if namespace != "" && j.Namespace != namespace {
			continue
		}
		summaries = append(summaries, JobSummary{
			ID:          j.ID,
			Namespace:   j.Namespace,
			Entry:       j.Entry,
			CreateIndex: j.CreateIndex,
			CreatedAt:   j.CreatedAt,
		})
	}

	// The bound is the same for every namespace (see JobsResponse.Bound), so
	// asking StatsIn about the filtered namespace — or "" when unfiltered — is
	// as good as asking about any other name; there is no per-namespace value
	// to disagree with it.
	writeJSON(w, http.StatusOK, JobsResponse{Jobs: summaries, Bound: d.Jobs.StatsIn(namespace).Bound}, pretty(q))
}
