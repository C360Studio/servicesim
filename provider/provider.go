package provider

import (
	"github.com/c360studio/servicesim/scenario"
)

// Name identifies a simulated provider. It is the listener's identity, not the
// scenario block's key: one listener may serve several scenario provider entries
// (Perplexity's Sonar and Agent surfaces are separate entries on one listener).
type Name string

// The simulated providers.
const (
	Exa        Name = "exa"
	Tavily     Name = "tavily"
	Perplexity Name = "perplexity"
)

// Route binds a ServeMux pattern to the fault budget it draws on and to the
// scenario field that budget is declared in.
type Route struct {
	// Pattern is a Go 1.22 ServeMux pattern, for example "POST /search".
	Pattern string

	// FaultKey is what attempt counting is keyed on, and — because the turn cursor
	// shares that counter rather than keeping a second one that could disagree
	// about which call this is — what turn selection is keyed on too. Two routes
	// that are aliases of one operation share a key, so a retry through the alias
	// draws on the same budget: Perplexity's /v1/sonar and /chat/completions are
	// both "perplexity:completions".
	FaultKey string

	// Fault selects this route's fault plan out of a scenario, for example
	// func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, "exa") }
	// (nil-safe on every hop). It exists so the fault engine never has to know
	// which scenario field a key maps to: the package that declares the key
	// declares the mapping next to it, and internal/faults stays at level 4 with no
	// knowledge of provider/exa, provider/tavily or provider/perplexity. A nil
	// Fault means the route declares no plan.
	Fault func(*scenario.Scenario) *scenario.Fault
}
