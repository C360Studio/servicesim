// Package main is servicesim's own binary: it registers the four reference
// profiles this repository ships and hands them to the composition root,
// github.com/c360studio/servicesim. This file is the registration site: it
// names profiles.Reference() — the one place besides profiles/profiles.go
// itself that lists the four vendors — and nothing else about them
// (docs/proposals/framework-seam.md, "internal/config and internal/server
// import NO profile package … cmd may — it is the registration site");
// everything else — flags, listeners, the admin surface, the journal,
// scenario loading, health checks, startup logs — is [servicesim.Main]'s,
// not this package's. A consumer composing their own binary follows exactly
// this shape with their own profiles (or any of ours) in place of
// profiles.Reference() below (see the servicesim package doc).
package main

import (
	"os"

	"github.com/c360studio/servicesim"
	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
)

func main() {
	os.Exit(servicesim.Main(
		servicesim.Build{
			Program: "servicesim",
			Version: servicesim.Version,
			Commit:  servicesim.GitCommit,
			BuiltAt: servicesim.BuildTime,
		},
		provider.MustSet(profiles.Reference()...),
	))
}
