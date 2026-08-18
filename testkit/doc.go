// Package testkit runs Servicesim's provider handlers in-process for Go consumer
// tests, with no Docker and no credentials.
//
// The whole surface is meant to fit in four lines of a consumer's test:
//
//	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProfiles(exa.Profile()))
//	adapter := myrepo.New(sim.URL(exa.Name), "test-key")
//	docs, err := adapter.Search(ctx, "report a")
//
// After that the journal is the evidence: testkit.AssertNoErrors(t,
// sim.Requests(exa.Name)[0]) proves the adapter sent a correct vendor
// request, not merely that it received a response. [WithProfiles] is how a
// test says which simulated APIs it needs — required, not defaulted, so a
// team simulating one vendor never pulls in every reference profile's
// contracts and goldens (owner decision D-5).
//
// The defaults are the production ones on purpose — a real clock, real delays,
// and a fault engine wired from the scenario over every declared route — so a
// scenario behaves identically in-process and in the container. Each option that
// trades fidelity away says so in its own documentation.
//
// Everything exported here is a compatibility obligation for consuming
// repositories that pin a release, so the surface is deliberately small: options,
// one Sim and its per-namespace Namespace lane, a set of assertions, and the
// journal and job type aliases without which a consumer outside this module
// could not name the types it is handed.
package testkit
