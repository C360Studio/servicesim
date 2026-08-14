package server

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/scenario"
)

// NewLogger builds the process logger from configuration. w receives the output;
// a nil w means os.Stderr, which is where a container's logs are read from.
//
// The default format is JSON because these logs are almost always read out of a
// CI container rather than a terminal, and a consumer diagnosing a failed
// adapter test greps them for a provider and a sequence number.
//
// It is exported for cmd/servicesim, which must build a logger before it can
// hand one to [New], and for tests that assert on startup events.
func NewLogger(cfg config.Config, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}

	var h slog.Handler
	switch cfg.LogFormat {
	case config.LogFormatText:
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// requestLogger returns the logger handed to every provider handler serving one
// scenario.
//
// The scenario name is attached here rather than at each call site because the
// per-request event is emitted by provider.Handle, which knows the provider, the
// route and the journal entry but has no reason to know which scenario is
// loaded. A consumer reading one CI log stream from a container that serves
// several scenarios to several concurrent tests needs it to interpret an
// outcome, and it is the field that makes the namespace on the same event
// meaningful: namespace says which test, scenario says which behaviour.
func requestLogger(logger *slog.Logger, s *scenario.Scenario) *slog.Logger {
	name := ""
	if s != nil {
		name = s.Name
	}
	return logger.With(slog.String("scenario", name))
}

// logFindings emits one event per scenario validation finding, in declaration
// order, at the level its severity maps to.
//
// Every finding is logged, not just the fatal ones, and it happens before [New]
// decides whether to fail: the reader of a failed startup needs the warnings
// that led up to the error as much as the error itself, and a warning that only
// ever appears on /__admin/scenario is invisible to the person reading a
// container that refused to start.
//
// name and source are both carried because they answer different questions when
// a directory of scenarios is loaded: name is the /x/<scenario> selector the
// finding belongs to, and source is the file it was read from. A reader with one
// of them and a directory of twenty scenarios has to guess.
func logFindings(logger *slog.Logger, report scenario.Report, name, source string) {
	for _, f := range report.Findings {
		level := slog.LevelWarn
		if f.Severity == scenario.SeverityError {
			level = slog.LevelError
		}
		logger.LogAttrs(context.Background(), level, "scenario.finding",
			slog.String("severity", string(f.Severity)),
			slog.String("code", f.Code),
			slog.String("path", f.Path),
			slog.String("scenario", name),
			slog.String("source", source),
			slog.String("message", f.Message))
	}
}
