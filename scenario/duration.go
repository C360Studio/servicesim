package scenario

import (
	"strconv"
	"time"
)

// Duration is a time.Duration that reads from a YAML scalar such as "250ms".
// It implements encoding.TextUnmarshaler, which yaml.v3 honours for scalar nodes
// (verified against yaml.v3 v3.0.1) and which encoding/json honours too.
type Duration time.Duration

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalText renders the value as a Go duration string so a decoded scenario
// round-trips through YAML.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// MarshalJSON renders the duration as a millisecond count so journal consumers in
// other languages do not have to parse Go duration syntax.
func (d Duration) MarshalJSON() ([]byte, error) {
	ms := time.Duration(d).Milliseconds()
	return []byte(strconv.FormatInt(ms, 10)), nil
}
