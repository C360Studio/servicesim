// Package contracts is the library a profile — in this repository or built
// against it out of tree — uses to keep its own golden wire fixtures honest
// against a dated provenance record.
//
// It holds no embedded data of its own (Phase 10 unit 6 genericised it over
// fs.FS): each profile embeds its own bundle — see, for the four reference
// profiles, profiles/<name>/profile.go's `//go:embed contracts` — and passes
// the resulting fs.FS to [Read], [Goldens], [Provenance], [ProviderSpec],
// [OldestVerified] and [Conform]. contracts imports no other package in this
// module (house rule 7's no-privilege rule: it sits beneath provider and
// profiles, so it may import neither), and everything above it calls into
// it, never the reverse — profiles/*/contract_test.go for the vendor-specific
// wire pins, and testkit.ValidateProfile for the generic discipline
// [Conform] runs.
//
// A golden is only trustworthy if a reviewer can answer one question when it
// changes: did the vendor change, or did we? That question is unanswerable
// without a record of what the shape was checked against and when, so every
// fixture in a well-formed bundle has an entry in its provenance.yaml naming
// the documentation URL, the verification date, and whether the shape is
// vendor-documented or simulator-chosen. A golden with no provenance entry is
// the failure mode [Conform] exists to catch.
//
// The authority on every field name and JSON type is the README.md beside
// each bundle, which records what the vendor's live documentation actually
// said on the verification date. When any other document in a repository
// disagrees with it, including a design plan, the README is right.
package contracts
