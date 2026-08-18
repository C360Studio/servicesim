package acme

import (
	"embed"
	"io/fs"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Name is this listener's identity: the base-URL env prefix (ACME_BASE_URL),
// the flag and env var port name (-acme-port, SERVICESIM_ACME_PORT), the
// journal's "provider" field, and the scenario's `providers: acme:` key.
const Name provider.Name = "acme"

// defaultPort is Acme's default listener port. A real profile picks one that
// does not collide with any listener it expects to run alongside — this
// example composes with servicesim's own exa (8081), so 8090 stays clear of
// every reference profile's 8081-8084 range.
const defaultPort = 8090

// contractsFS embeds Acme's own contract bundle — goldens, provenance.yaml
// and a README — beside this package, exactly where a real profile's lives
// (docs/proposals/framework-seam.md, "Profile.Contracts": "the four
// reference profiles each embed their own bundle... an out-of-tree profile
// does the same beside its own package").
//
//go:embed contracts
var contractsFS embed.FS

// Profile returns Acme's registration record: everything servicesim.Main and
// testkit.WithProfiles need to serve, validate and journal this vendor,
// composed from the fields handler.go, render.go and errors.go declare.
//
// Every field here is one a real profile sets; docs/building-a-profile.md
// walks each one in turn under "step 2, write the package".
func Profile() provider.Profile {
	sub, err := fs.Sub(contractsFS, "contracts")
	if err != nil {
		// Unreachable: "contracts" is the fixed, valid fs.Sub pattern the
		// //go:embed directive above always populates.
		panic("acme: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:    Name,
		Title:   "Acme",
		Summary: "the Acme answer API (a worked example vendor, not a real one)",
		Port:    defaultPort,

		Handlers: handlers(),
		Routes:   Routes(),
		Validators: map[string]provider.Validator{
			string(Name): validator{},
		},
		ErrorBody: ErrorBody,

		// DefaultAuth is required, opposite MCP's optional default
		// (docs/proposals/framework-seam.md documents both defaults side by
		// side): Acme documents Authorization: Bearer on every route, so a
		// request presenting no credential at all is refused before it ever
		// reaches turn selection. This is what exercises the 401 convention
		// testkit.ValidateProfile's MissingCredential subtest checks.
		DefaultAuth: scenario.AuthRequired,

		Contracts: sub,

		// Hosts is the denylist scripts/lint-no-live-hosts.sh (in this
		// repository) and testkit.AssertNoLiveHosts (in an adopter's own CI)
		// refuse to see in scenario or fixture data. A real profile puts the
		// vendor's real, live hostname here, never a placeholder — see
		// profiles/exa/profile.go's own Hosts field for what that looks
		// like — so that a base URL typo'd into a scenario fixture is caught
		// before it ever dials out. Acme is fictional, so this is a reserved
		// .test domain instead, which is what proves the guard treats a
		// profile's own declared hosts as data rather than as a
		// hand-maintained list only servicesim's own four vendors can join.
		//
		// The trailing marker on the line below is the same escape hatch
		// every reference profile's own Hosts field carries (see, for
		// example, profiles/tavily/profile.go): AssertNoLiveHosts's pattern
		// is the union of every registered profile's own Hosts, so the ONE
		// place that is expected to name them literally — this declaration
		// itself — would otherwise trip the very guard it feeds.
		Hosts: []string{"api.acme.test"}, // servicesim:allow-live-host -- the DENYLIST entry Profile.Hosts exists to carry; never dialled.

		// DerivedIDs names the one response field both routes render from
		// provider.Hex32 rather than from the scenario: a golden compare
		// that pins request_id exactly would break on every re-record for no
		// reason connected to the vendor's actual documented shape.
		DerivedIDs: []string{"request_id"},

		// CredentialNames widens the journal's redaction vocabulary beyond
		// Authorization (which house rule 4's fixed tables already mask) to
		// a header this vendor invented: x-acme-key. Declaring it here is
		// the whole of what a profile author does for house rule 4 — no
		// redaction code of Acme's own exists anywhere in this package.
		CredentialNames: []string{"x-acme-key"},
	}
}
