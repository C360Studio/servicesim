// Package tavily simulates the Tavily search API on its own listener:
// POST /search, POST /extract, and the asynchronous POST /research +
// GET /research/{request_id} surface (research.go). /search's path
// deliberately collides with Exa's, which is why each provider gets a
// listener of its own rather than a shared mux with a host-based hack.
//
// The package declares its routes and the fault budget each one draws on
// (handler.go, extract.go, research.go); it validates a request against the
// documented properties of each live schema (request.go, extract.go); it
// projects the scenario's canonical sources into Tavily's wire shapes
// (render.go, response.go, extract.go); and it renders the one error envelope
// every documented Tavily status uses, {"detail":{"error":"…"}} (errors.go).
// Everything else — the request lifecycle, the journal, fault execution, turn
// selection and mux construction — belongs to the provider package.
//
// Details of the wire contract are load-bearing and easy to get wrong, which
// is why contracts/tavily/README.md, not this package's history, is the
// authority on them:
//
//   - response_time is a JSON NUMBER. The plan document encodes it as the
//     string "1.15"; the schema declares type number, format float, and a
//     string breaks every typed consumer. render_test.go asserts it at the byte
//     level, because a struct-level assertion cannot tell the two apart.
//   - /search's images[] items are OBJECTS, {url, description}, never bare URL
//     strings, at the top level and inside a result alike. /extract's
//     results[].images is the opposite: bare URL strings, per its own
//     documented response schema — the two routes do not share this shape
//     despite sharing a field name.
//   - answer is always emitted, null when the request did not ask for one. The
//     200 schema lists it as required while its description says it appears
//     only when include_answer was requested; a present null breaks fewer
//     consumers than a sometimes-absent required key.
//   - POST /extract resolves each requested URL against the selected turn's
//     ordinary /search results first, then the corpus by exact URL match — it
//     declares no results of its own. See ExtractProjection's doc comment.
//
// The projection type this package decodes from a scenario turn lives here
// rather than in the scenario package. Under the open provider registry a
// turn's respond body stays an undecoded YAML node until the provider package
// that understands it calls Turn.DecodeProjection, which is what keeps scenario
// free of any knowledge of what a Tavily result looks like. Projection bodies
// are therefore validated by [Validator], which internal/server runs before
// readiness reports true, so a fixture with a bad field still fails at boot
// rather than on the first request.
package tavily
