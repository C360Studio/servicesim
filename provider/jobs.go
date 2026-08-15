package provider

// Job identifier bounds.
const (
	// MaxJobIDLen bounds a job identifier taken from a request path.
	//
	// It is deliberately shorter than maxLaneValueLen (128), so a well-formed
	// identifier can never be the thing appendLanePart drops for length. If the
	// two were equal, a legal identifier at the boundary would silently fall back
	// to the shared route lane and two jobs would answer from one cursor.
	//
	// Both vendors fit comfortably: Exa mints "run_" plus 32 hex characters (36),
	// Tavily a UUID (36).
	MaxJobIDLen = 64
)

// Finding codes the async job surfaces raise.
const (
	// CodeJobIDInvalid is raised when a route's path-derived lane discriminator
	// resolves to nothing usable — an absent segment, or one that is not a
	// well-formed identifier.
	//
	// It is deliberately NOT CodeTurnKeyUnresolved. A Route.LaneFrom extractor is
	// declared in Go by the provider package, not in YAML by a scenario author,
	// so reporting it on the field "turn_key" would send a reader hunting through
	// their scenario for a key they never wrote and cannot add. The field is the
	// path wildcard's own name, which is the part of the request the client
	// actually got wrong.
	CodeJobIDInvalid = "job.id_invalid"
)

// ValidJobID reports whether id is safe to use as a job identifier and as a
// component of a lane key: one to [MaxJobIDLen] characters of ASCII letters,
// digits, '-' and '_'.
//
// The charset is load-bearing rather than cosmetic. A path value reaches this
// after http.ServeMux has PERCENT-DECODED it, so "GET /agent/runs/run%2Fabc"
// arrives as "run/abc" — a literal separator inside what the caller believes is
// one segment. A lane key joins its namespace with '/', and SplitCursorKey
// splits on it, so an unvalidated identifier can be re-split into a different
// (namespace, key) pair and read another namespace's state. Rejecting the
// separators closes that at the only point where the value is still known to be
// one segment.
//
// It is a shape check, not a scheme check. It says an identifier COULD be one
// this simulator minted; it cannot say that one WAS. Exa's "run_" prefix and
// Tavily's UUID layout are each provider knowledge, and a caller wanting that
// precision has to ask the provider package.
func ValidJobID(id string) bool {
	if id == "" || len(id) > MaxJobIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
