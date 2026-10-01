package scenario

import "testing"

// TestFaultAttemptDeliversBody pins the delivery predicate over the shapes the
// fault executor treats differently. The same table, with the same expectations,
// is checked against a real HTTP client by provider's
// TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier, which is what
// stops this one being a belief written down: if the executor and this method
// ever disagree, that test fails, not this one.
func TestFaultAttemptDeliversBody(t *testing.T) {
	t.Parallel()

	body := map[string]any{"scripted": true}
	tests := []struct {
		name string
		a    FaultAttempt
		want bool
	}{
		{"an empty attempt", FaultAttempt{}, true},
		{"an explicit 2xx", FaultAttempt{Status: 201}, true},
		{"a 205", FaultAttempt{Status: 205}, true},
		{"a 103", FaultAttempt{Status: 103}, true},
		{"a pure delay", FaultAttempt{Delay: Duration(5)}, true},
		{"delay_after_headers", FaultAttempt{DelayAfterHeaders: Duration(5)}, true},
		{"error and tag below 400", FaultAttempt{Error: "boom", Tag: "T"}, true},
		{"kind status below 400", FaultAttempt{Kind: FaultStatus, Status: 200}, true},
		{"extra_fields", FaultAttempt{Kind: FaultExtraFields, ExtraFields: ExtraFields{"x": 1}}, true},
		{"wrong_content_type", FaultAttempt{Kind: FaultWrongContentType, ContentType: "text/plain"}, true},
		{"oversized_body", FaultAttempt{Kind: FaultOversizedBody, BodyBytes: 4096}, true},
		{"oversized_body inferred from body_bytes", FaultAttempt{BodyBytes: 4096}, true},
		{"stream_disconnect", FaultAttempt{Kind: FaultStreamDisconnect}, true},
		{"stream_truncate_chunk", FaultAttempt{Kind: FaultStreamTruncateChunk}, true},
		{"stream_stall", FaultAttempt{Kind: FaultStreamStall}, true},
		{"stream_disconnect declaring a status", FaultAttempt{Kind: FaultStreamDisconnect, Status: 503}, true},

		{"body override with no status", FaultAttempt{Body: body}, false},
		{"body override at 201", FaultAttempt{Status: 201, Body: body}, false},
		{"extra_fields over a body override", FaultAttempt{Kind: FaultExtraFields, Body: body}, false},
		{"wrong_content_type over a body override", FaultAttempt{Kind: FaultWrongContentType, Body: body}, false},
		{"oversized_body over a body override", FaultAttempt{Kind: FaultOversizedBody, BodyBytes: 4096, Body: body}, false},

		{"a 101", FaultAttempt{Status: 101}, false},
		{"a 204", FaultAttempt{Status: 204}, false},
		{"a 304", FaultAttempt{Status: 304}, false},

		{"a 400", FaultAttempt{Status: 400}, false},
		{"a 429", FaultAttempt{Status: 429}, false},
		{"a 500 with error and tag", FaultAttempt{Status: 500, Error: "boom", Tag: "T"}, false},
		{"extra_fields at 429", FaultAttempt{Kind: FaultExtraFields, Status: 429}, false},
		{"wrong_content_type at 503", FaultAttempt{Kind: FaultWrongContentType, Status: 503}, false},
		{"oversized_body at 503", FaultAttempt{Kind: FaultOversizedBody, BodyBytes: 4096, Status: 503}, false},

		{"empty_body", FaultAttempt{Kind: FaultEmptyBody}, false},
		{"invalid_json", FaultAttempt{Kind: FaultInvalidJSON}, false},
		{"invalid_json inferred from raw_body", FaultAttempt{RawBody: "not json"}, false},
		{"close_before_headers", FaultAttempt{Kind: FaultCloseBeforeHeaders}, false},
		{"truncate_body", FaultAttempt{Kind: FaultTruncateBody, TruncateAfterBytes: 4}, false},
		{"truncate_body inferred from reset", FaultAttempt{Reset: true}, false},

		{"a kind this predicate does not name", FaultAttempt{Kind: FaultKind("not_a_kind")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.a.DeliversBody(); got != tc.want {
				t.Errorf("DeliversBody() = %v, want %v (effective kind %q)", got, tc.want, tc.a.EffectiveKind())
			}
		})
	}
}
