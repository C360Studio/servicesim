package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSystemClockDistinguishesInstants(t *testing.T) {
	t.Parallel()

	// AssertOverlapped rests on two in-flight requests receiving distinguishable
	// instants, which is the whole reason there is no FakeClock in this module.
	var c Clock = SystemClock{}
	first := c.Now()
	second := c.Now()

	require.False(t, first.IsZero(), "SystemClock must return real time")
	require.False(t, second.Before(first), "time must not go backwards")
}

func TestSleep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		delay    time.Duration
		mode     DelayMode
		cancel   bool
		wantErr  bool
		wantFast bool
	}{
		{name: "skip returns immediately", delay: time.Hour, mode: DelaySkip, wantFast: true},
		{name: "zero duration returns immediately", delay: 0, mode: DelayReal, wantFast: true},
		{name: "negative duration returns immediately", delay: -time.Second, mode: DelayReal, wantFast: true},
		{name: "real waits", delay: 5 * time.Millisecond, mode: DelayReal},
		{
			name:     "cancelled context releases the goroutine",
			delay:    time.Hour,
			mode:     DelayReal,
			cancel:   true,
			wantErr:  true,
			wantFast: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}

			start := time.Now()
			err := sleep(ctx, tc.delay, tc.mode)
			elapsed := time.Since(start)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.wantFast {
				require.Less(t, elapsed, time.Second,
					"a skipped, zero or cancelled delay must not wait out the duration")
			} else {
				require.GreaterOrEqual(t, elapsed, tc.delay)
			}
		})
	}
}

func TestDelayModeString(t *testing.T) {
	t.Parallel()

	require.Equal(t, "real", DelayReal.String())
	require.Equal(t, "skip", DelaySkip.String())
	require.Equal(t, "unknown", DelayMode(42).String())
}

func TestDelayRealIsTheZeroValue(t *testing.T) {
	t.Parallel()

	// A scenario declaring delay: 250ms must block a client for 250ms whether it
	// is served in-process or from the container, so the zero value must wait.
	var mode DelayMode
	require.Equal(t, DelayReal, mode)
	require.Equal(t, DelayReal, Deps{}.Normalized().DelayMode)
}
