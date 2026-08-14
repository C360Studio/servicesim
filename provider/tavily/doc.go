// Package tavily simulates the Tavily search API on its own listener: one
// route, POST /search, whose path deliberately collides with Exa's and is only
// separable by giving each provider a listener of its own.
//
// The package owns four things and nothing else. It declares its route and the
// fault budget that route draws on (handler.go); it validates a request against
// the twenty documented properties of the live search schema (request.go); it
// projects the scenario's canonical sources into Tavily's wire shape
// (render.go, response.go); and it renders the one error envelope every
// documented Tavily status uses, {"detail":{"error":"…"}} (errors.go).
// Everything else — the request lifecycle, the journal, fault execution, turn
// selection and mux construction — belongs to the provider package.
//
// Three details of the wire contract are load-bearing and easy to get wrong,
// which is why contracts/tavily/README.md, not this package's history, is the
// authority on them:
//
//   - response_time is a JSON NUMBER. The plan document encodes it as the
//     string "1.15"; the schema declares type number, format float, and a
//     string breaks every typed consumer. render_test.go asserts it at the byte
//     level, because a struct-level assertion cannot tell the two apart.
//   - images[] items are OBJECTS, {url, description}, never bare URL strings,
//     at the top level and inside a result alike.
//   - answer is always emitted, null when the request did not ask for one. The
//     200 schema lists it as required while its description says it appears
//     only when include_answer was requested; a present null breaks fewer
//     consumers than a sometimes-absent required key.
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
