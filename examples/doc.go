// Package examples is a worked consumer of Servicesim: a small research adapter
// in adapter.go, and the tests a consuming team would write against it in the
// files beside it.
//
// It is meant to be read and copied. Every file here is compiled by
// `go build ./...` and run by `go test -race ./...` on purpose — an example that
// is not built is an example that rots, and the first person to trust it is the
// one who discovers it stopped compiling three releases ago. Keeping these files
// inside the main module is what makes CI the thing that notices instead.
//
// # What a real consumer does differently
//
// A consuming repository imports this module by path:
//
//	import (
//	    "github.com/c360studio/servicesim/provider"
//	    "github.com/c360studio/servicesim/provider/exa"
//	    "github.com/c360studio/servicesim/testkit"
//	)
//
// and gets exactly the surface these files use. `testkit.WithProfiles(exa.Profile(), ...)` is how a test
// says which simulated APIs it needs — required, not defaulted, so a team simulating one vendor never
// pulls in every reference profile's contracts and goldens; the four reference profiles live at
// provider/exa, provider/tavily, provider/perplexity and provider/mcp, each exporting Profile(). Nothing
// here may reach into github.com/c360studio/servicesim/internal/..., because a consumer outside this
// module cannot: Go's own import rules forbid it. An example that used an internal package would be
// teaching something impossible, so imports_test.go parses every file in this directory and fails if
// one ever does. A profile package import (provider/exa and friends) is fine — it is what a consumer
// does.
//
// The adapter itself imports nothing from Servicesim at all. That is the shape a
// consumer wants: production code has no dependency on the simulator, and only
// the test files know it exists.
//
// # Where to start
//
//   - adapter_test.go — the canonical first test: prove the adapter sent the
//     correct vendor request, not merely that it got a response.
//   - fusion_test.go — many providers at once: deduplicating one canonical source
//     that arrived from three of them, proving the calls really were concurrent,
//     and classifying a 429 as retryable.
//   - namespace_test.go — parallel subtests sharing one simulator, each in its
//     own state lane.
//   - mcpclient.go and mcp_test.go — a minimal Streamable HTTP MCP client: the
//     fourth profile is a protocol, not a vendor, so its worked example covers
//     the JSON-RPC envelope, an SSE tools/call answer, catalogue drift across
//     turns, and the malicious-content corpus on MCP's own content shapes.
package examples
