package scenario

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Nullable is a three-state YAML/JSON scalar: absent, explicit null, or a string.
// It exists because several verified fields are anyOf[string, null] and a
// consumer that only ever sees a field absent has not exercised the null branch.
//
// The zero value is the absent state, so a Nullable field that a scenario never
// mentions marshals away under encoding/json's omitzero option.
type Nullable struct {
	set   bool
	null  bool
	value string
}

// SetNullable returns a Nullable holding v.
func SetNullable(v string) Nullable {
	return Nullable{set: true, value: v}
}

// NullNullable returns a Nullable that renders as an explicit JSON null.
func NullNullable() Nullable {
	return Nullable{set: true, null: true}
}

// IsZero reports whether the value is absent, which is what encoding/json's
// omitzero option consults.
func (n Nullable) IsZero() bool {
	return !n.set
}

// IsNull reports whether the value is present and explicitly null.
func (n Nullable) IsNull() bool {
	return n.set && n.null
}

// Get returns the string and whether a non-null value is present.
func (n Nullable) Get() (string, bool) {
	if !n.set || n.null {
		return "", false
	}
	return n.value, true
}

// UnmarshalYAML accepts a string scalar or absence.
//
// It cannot observe an explicit YAML null: yaml.v3 v3.0.1 short-circuits a null
// node in decoder.prepare and never calls a custom unmarshaler for one, so
// "field: null" and an absent field both arrive here as the zero value. That
// costs nothing on the wire — MarshalJSON renders null for both states — and it
// means a projection that wants a guaranteed explicit null asks for it with a
// dedicated flag, the way Source.AuthorNull does, rather than by writing null in
// the YAML and hoping.
func (n *Nullable) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == 0 {
		*n = Nullable{}
		return nil
	}
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected a string or null, got a %s", value.Line, nodeKindName(value.Kind))
	}
	if value.Tag == "!!null" {
		*n = NullNullable()
		return nil
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("line %d: expected a string or null: %w", value.Line, err)
	}
	*n = SetNullable(s)
	return nil
}

// MarshalYAML renders the three states back to YAML so that a decoded scenario
// round-trips, which the schema round-trip test depends on.
func (n Nullable) MarshalYAML() (any, error) {
	if !n.set || n.null {
		return nil, nil
	}
	return n.value, nil
}

// MarshalJSON renders null for the null and absent states.
func (n Nullable) MarshalJSON() ([]byte, error) {
	if !n.set || n.null {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}
