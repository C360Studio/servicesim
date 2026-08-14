// Command servicesim runs the deterministic Exa, Tavily and Perplexity
// simulator: one process, one admin listener and one listener per provider.
//
// # Three process modes
//
// The binary does one of three things and then exits, chosen by flags alone:
//
//   - --version prints the build information injected through ldflags and exits
//     0. CI runs it as a build-job smoke test, so it must succeed on a binary
//     built with no scenario, no configuration and no network.
//   - --healthcheck performs one local HTTP GET against the admin listener and
//     exits 0 or 1. The image's HEALTHCHECK invokes it, and it is the reason the
//     runtime stage can be shell-less scratch: without it the image would need
//     wget or curl, and therefore a distribution underneath.
//   - Otherwise the process serves, until SIGINT or SIGTERM.
//
// Both process modes are flag-only, never environment-driven. An environment
// variable that could silently turn a running simulator into a health probe
// would be a footgun in exactly the setting Servicesim runs in — another
// repository's CI, configured by someone who has never read this file.
//
// # Startup order is the acceptance criterion
//
// The scenario is opened, decoded, envelope-validated and projection-validated
// by [github.com/c360studio/servicesim/internal/server.New] before a single
// listener binds, and readiness reports true only once every listener is
// accepting. A scenario with an error finding therefore fails the process with a
// non-zero exit rather than serving a subtly wrong contract — a simulator that
// answers plausibly-but-wrongly costs a consumer far more than one that refuses
// to start.
//
// # Exit codes
//
// 0 is success, which includes a clean shutdown after a signal. 2 is a usage or
// configuration error. 1 is everything else: a scenario that failed to load or
// validate, a listener that could not bind, and a failed health probe. A probe
// invocation never reports 2, because Docker reserves that code.
package main
