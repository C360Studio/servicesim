// Package contracts embeds Servicesim's golden wire fixtures and the dated
// provenance record that says where each one's shape came from.
//
// It is the lowest package in the module: it holds data, imports no other
// package in this repository, and nothing in this repository imports it except
// its own test. In particular testkit does not — testkit.AssertGoldenJSON
// compares a consumer's golden against the path it is given and knows nothing
// about provenance, because a consumer's goldens live in the consumer's
// repository.
//
// A golden is only trustworthy if a reviewer can answer one question when it
// changes: did the vendor change, or did we? That question is unanswerable
// without a record of what the shape was checked against and when, so every
// fixture here has an entry in its directory's provenance.yaml naming the
// documentation URL, the verification date, and whether the shape is
// vendor-documented or simulator-chosen. A golden with no provenance entry is
// the failure mode this package exists to prevent, and contracts_test.go fails
// the build on one.
//
// The authority on every field name and JSON type is the README.md in each
// provider directory, which records what the vendor's live documentation
// actually said on the verification date. When any other document in this
// repository disagrees with it, including the plan, the README is right.
package contracts
