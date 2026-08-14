// Package scenario defines the versioned YAML scenario schema, loads it, validates
// it, and resolves source references into pointers so that request handling never
// performs a lookup that can fail.
//
// A scenario is one deterministic corpus of canonical sources plus an open registry
// of per-provider blocks that project that corpus onto the wire. The registry is
// deliberately open: adding a provider to Servicesim must never require editing this
// package, and a scenario file written today must keep loading when new providers
// arrive.
//
// This package is level 1. It knows what an envelope looks like — authentication,
// validation, faults, turns — and deliberately does not know what a search result
// looks like. Each turn's projection body is left as an undecoded [gopkg.in/yaml.v3.Node]
// and is decoded by the owning provider package through [Turn.DecodeProjection].
// Validation is split the same way: [Scenario.Validate] checks the envelope, and
// provider.ValidateScenario checks the bodies before readiness reports true.
package scenario
