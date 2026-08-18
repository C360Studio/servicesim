package acme

import (
	"strconv"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// AnswerResponse is POST /v1/answer's wire shape.
type AnswerResponse struct {
	// RequestID is a route-derived identifier — deterministic, never a clock
	// or math/rand, on the same terms Exa's requestId and Tavily's
	// request_id are — that Profile's DerivedIDs names so a consumer's
	// golden compare can prune it rather than pin it
	// (testkit.GoldenDerivedIDs).
	RequestID string `json:"request_id"`

	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence,omitempty"`
}

// StatusResponse is GET /v1/status's wire shape.
type StatusResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

// projectionBody is how the shared corpus renders through the Acme API. It
// is the decoded form of a turn's `respond:` body — one struct serving both
// routes, because both are addressed through the one "acme" scenario entry
// (neither Route below sets its own Entry). The reserved envelope keys
// (kind, auth, validation, fault, turns, turn_key) are stripped by the
// scenario loader before this is decoded, so they are deliberately absent
// here.
type projectionBody struct {
	// Answer is a pointer so a scenario can distinguish "this scenario has
	// no answer to give" (nil, renders "") from "the answer is the empty
	// string" (an explicit ""). Read only by handleAnswer.
	Answer *string `yaml:"answer,omitempty"`

	// Confidence projects POST /v1/answer's own optional field. Read only by
	// handleAnswer.
	Confidence float64 `yaml:"confidence,omitempty"`

	// Status projects GET /v1/status's one field, defaulting to "operational"
	// when the scenario leaves it unset. Read only by handleStatus.
	Status string `yaml:"status,omitempty"`

	// OmitFields drops named response fields that would otherwise be
	// present. ExtraFields adds ones that would not.
	OmitFields  []string             `yaml:"omit_fields,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// requestID derives POST /v1/answer and GET /v1/status's shared identifier
// shape: a route-derived, 32-character lowercase hex string, the same shape
// Exa documents for requestId. It reads the scenario's seed, the listener
// name, the route's fault key and the claimed call index — never a clock,
// never math/rand — so the same request at the same call position always
// renders the same bytes (house rule 2).
func requestID(x *provider.Exchange) string {
	return provider.Hex32(x.Deps.Scenario.SeedKey(), string(x.Provider), x.Route.FaultKey, strconv.Itoa(x.CallIndex()))
}

// renderAnswer renders p through provider.Render, never encoding/json
// directly: a bare json.Marshal escapes "&" as "\u0026" and can re-render an
// integral float64 as "1e+06", both wire-contract changes dressed up as
// formatting details (house rule 2). See testkit.AssertRenderShape, which
// this profile's own conformance test runs against every response this
// package produces.
func renderAnswer(x *provider.Exchange, p *projectionBody) ([]byte, error) {
	answer := ""
	if p.Answer != nil {
		answer = *p.Answer
	}
	resp := AnswerResponse{RequestID: requestID(x), Answer: answer, Confidence: p.Confidence}
	return provider.Render(resp, p.ExtraFields, p.OmitFields)
}

// renderStatus renders p through provider.Render, for the same reason
// renderAnswer does.
func renderStatus(x *provider.Exchange, p *projectionBody) ([]byte, error) {
	status := p.Status
	if status == "" {
		status = "operational"
	}
	resp := StatusResponse{RequestID: requestID(x), Status: status}
	return provider.Render(resp, p.ExtraFields, p.OmitFields)
}
