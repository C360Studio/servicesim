// Package main is servicesim's own binary: it registers the four reference
// profiles this repository ships and hands them to the composition root,
// github.com/c360studio/servicesim. This file is the ONLY place this build
// names a vendor (docs/proposals/framework-seam.md, "internal/config and
// internal/server import NO profile package … cmd may — it is the
// registration site"); everything else — flags, listeners, the admin
// surface, the journal, scenario loading, health checks, startup logs — is
// [servicesim.Main]'s, not this package's. A consumer composing their own
// binary follows exactly this shape with their own profiles (or any of
// ours) in place of the four below (see the servicesim package doc).
package main

import (
	"os"

	"github.com/c360studio/servicesim"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
)

func main() {
	os.Exit(servicesim.Main(
		servicesim.Build{
			Program: "servicesim",
			Version: servicesim.Version,
			Commit:  servicesim.GitCommit,
			BuiltAt: servicesim.BuildTime,
		},
		provider.MustSet(exa.Profile(), tavily.Profile(), perplexity.Profile(), mcp.Profile()),
	))
}
