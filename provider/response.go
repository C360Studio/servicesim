package provider

import (
	"net/http"

	"github.com/c360studio/servicesim/scenario"
)

// Response is what a provider handler decided to send, before faults are applied.
type Response struct {
	Status int
	Header http.Header

	// Body is the fully rendered response bytes, extras already merged.
	//
	// Body must STILL be populated when Stream is set. It is what a
	// non-streaming caller of the same turn receives, and what a
	// stream-suppressing fault writes (see suppressStream in
	// fault_exec.go). The two are rendered from one projection, so a
	// scenario cannot quote one cost when it streams and another when it
	// does not.
	Body []byte

	// Stream, when non-nil, is written instead of Body as a Server-Sent
	// Events sequence. Nil for every non-streaming response, which is every
	// response this repository served before streaming existed.
	Stream *Stream

	// Label names the selection for the journal and logs, for example
	// "exa.search.ok" or "exa.error.INVALID_REQUEST_BODY".
	Label string

	// FaultEligible is set only when routing, authentication and validation all
	// passed. A rejected request must not consume a retry budget.
	FaultEligible bool

	// FaultBody builds the provider-shaped body for a fault attempt, and is how
	// §2.5's rule that "the body is provider-shaped and built by the provider
	// package, not here" is honoured without provider knowledge leaking into fault
	// execution. It is called only when an attempt actually applies, which is
	// after the handler has returned and therefore after the handler could have
	// built the body itself.
	//
	// It is optional. When it is nil, or returns nil, the attempt's own `body:`
	// wins if it declares one, and otherwise the rendered scenario body is written
	// under the fault's status — honest, and visibly not the vendor's error shape.
	FaultBody func(scenario.FaultAttempt) []byte
}

// Handler is a provider route handler: it turns an Exchange into a Response.
type Handler func(*Exchange) Response
