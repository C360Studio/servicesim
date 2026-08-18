// Command acmesim is a consumer's own binary: one foreign profile (acme)
// composed with one of servicesim's own reference profiles (exa), served
// through the exported composition root — proving that composing "one of
// ours" alongside an adopter's own profile is ordinary, not a special case
// (docs/proposals/framework-seam.md, "How a consumer composes a binary and
// an image"). Nothing else in this module imports servicesim's own
// profiles/*; docs/building-a-profile.md points readers at them by name
// instead of repeating their code here.
package main

import (
	"os"

	"github.com/c360studio/servicesim"
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/provider"

	"example.test/acmesim/acme"
)

// version is overridden at build time the same way servicesim's own
// cmd/servicesim/main.go's is:
// -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	os.Exit(servicesim.Main(
		servicesim.Build{Program: "acmesim", Version: version},
		provider.MustSet(acme.Profile(), exa.Profile()),
	))
}
