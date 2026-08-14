// Package config resolves Servicesim's runtime configuration from flags,
// environment variables and defaults, in that order of precedence.
//
// Three properties define the package.
//
// Precedence is explicit flag > environment variable > built-in default, and
// "explicit" means what the operator typed, not what the value happens to be.
// [flag.FlagSet] cannot distinguish "set to the default value" from "not set",
// so [Load] registers every flag with its built-in default, parses, collects the
// explicitly-set names with FlagSet.Visit, and applies the environment only to
// the names Visit did not report. Without that step "--exa-port 8081" and an
// unset flag are indistinguishable and SERVICESIM_EXA_PORT would silently
// override an explicit flag.
//
// Every bound is validated before it is returned. A negative JournalCapacity,
// MaxRequestBytes or MaxJournalBodyBytes is an error naming the flag, not a
// value passed downstream: otherwise --journal-capacity -1 panics inside
// make([]Entry, capacity) at startup and the container reports unhealthy with a
// stack trace, and a negative --max-request-bytes clamps http.MaxBytesReader to
// zero, silently rejecting every request as too large.
//
// Scenario paths are confined. [Config.OpenScenario] resolves a mounted scenario
// through [os.Root], which refuses to traverse outside the root at the syscall
// level — including through a symlink, which a filepath.Clean prefix check does
// not catch. The binary must open a mounted scenario this way and never through
// scenario.Load, which performs no containment at all.
//
// [Load] takes an environment lookup rather than calling os.Getenv so its tests
// are hermetic and can run in parallel without t.Setenv serialising them;
// cmd/servicesim passes os.LookupEnv.
package config
