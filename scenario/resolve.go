package scenario

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Resolve builds the source and claim indexes and links every SourceRef reachable
// from the scenario's own decoded structures to its Source. It returns an error
// naming the first unresolvable reference by its YAML path, for example
// "providers.exa.results[2].source: unknown source \"source-z\"".
//
// Under the open provider registry a projection body is an undecoded node, so the
// references inside one are not reachable from here. The provider package resolves
// its own projection after decoding it, by calling [Scenario.ResolveRefs] on the
// typed value. Resolve is still what makes the indexes exist, and it is still what
// must run before any request is served.
//
// Resolve takes addresses of slice elements, so appending to Sources afterwards
// invalidates every resolved pointer.
func (s *Scenario) Resolve() error {
	if s == nil {
		return nil
	}

	s.bySourceID = make(map[string]*Source, len(s.Sources))
	s.byClaimID = make(map[string][]*Source)

	// Correct: address the slice element. Ranging by value would address the
	// per-iteration copy, so every projection would point at a value that is not
	// in s.Sources and that mutations never reach.
	for i := range s.Sources {
		src := &s.Sources[i]
		if _, dup := s.bySourceID[src.ID]; dup {
			return fmt.Errorf("sources[%d].id: duplicate source %q", i, src.ID)
		}
		s.bySourceID[src.ID] = src
	}
	for i := range s.Sources {
		src := &s.Sources[i]
		for j := range src.Claims {
			id := src.Claims[j].ID
			s.byClaimID[id] = append(s.byClaimID[id], src)
		}
	}

	if findings := s.ResolveRefs("", s); len(findings) > 0 {
		return fmt.Errorf("%s: %s", findings[0].Path, findings[0].Message)
	}
	return nil
}

// SourceByID returns the canonical source with the given ID.
func (s *Scenario) SourceByID(id string) (*Source, bool) {
	if s == nil || s.bySourceID == nil {
		return nil, false
	}
	src, ok := s.bySourceID[id]
	return src, ok
}

// SourcesForClaim returns every source asserting the given claim ID, in corpus
// declaration order.
func (s *Scenario) SourcesForClaim(claimID string) []*Source {
	if s == nil || s.byClaimID == nil {
		return nil
	}
	return s.byClaimID[claimID]
}

// ResolveRefs links every SourceRef reachable from v to its canonical Source and
// returns one finding per unresolvable reference, addressed by its YAML path
// beneath root.
//
// Provider packages call it on their own decoded projection, because the scenario
// package cannot see inside an undecoded projection body. Writing the walk once
// here is what keeps three provider packages from growing three subtly different
// resolution loops.
//
// v must be a pointer, or an addressable value reached through one; a SourceRef
// found in an unaddressable position cannot be written and is reported as a
// finding rather than silently skipped.
func (s *Scenario) ResolveRefs(root string, v any) []Finding {
	if s == nil || v == nil {
		return nil
	}
	w := refWalker{scenario: s}
	w.walk(reflect.ValueOf(v), root, 0)
	return w.findings
}

type refWalker struct {
	scenario *Scenario
	findings []Finding
}

var (
	sourceRefType = reflect.TypeOf(SourceRef{})
	timeType      = reflect.TypeOf(time.Time{})
)

func (w *refWalker) walk(v reflect.Value, path string, depth int) {
	if depth > 32 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		w.walk(v.Elem(), path, depth+1)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			// Map values are not addressable, so a SourceRef inside a map cannot
			// be resolved in place. No projection declares one; the walk still
			// descends so a future one is reported rather than silently ignored.
			w.walk(v.MapIndex(key), joinPath(path, fmt.Sprint(key.Interface())), depth+1)
		}
	case reflect.Struct:
		if v.Type() == sourceRefType {
			w.resolveRef(v, path)
			return
		}
		if v.Type() == timeType {
			return
		}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.PkgPath != "" {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "-" {
				continue
			}
			child := path
			if !containsInline(opts) {
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				child = joinPath(path, name)
			}
			w.walk(v.Field(i), child, depth+1)
		}
	default:
	}
}

func (w *refWalker) resolveRef(v reflect.Value, path string) {
	refPath := joinPath(path, "source")
	if !v.CanAddr() {
		w.findings = append(w.findings, Finding{
			Severity: SeverityError,
			Code:     "scenario.source.unaddressable",
			Path:     refPath,
			Message:  "source reference cannot be resolved in place; pass a pointer to the projection",
		})
		return
	}
	ref, _ := v.Addr().Interface().(*SourceRef)
	if ref.Ref == "" {
		w.findings = append(w.findings, Finding{
			Severity: SeverityError,
			Code:     "scenario.source.missing",
			Path:     refPath,
			Message:  "source reference is empty",
		})
		return
	}
	target, found := w.scenario.SourceByID(ref.Ref)
	if !found {
		w.findings = append(w.findings, Finding{
			Severity: SeverityError,
			Code:     "scenario.source.unknown",
			Path:     refPath,
			Message:  fmt.Sprintf("unknown source %q", ref.Ref),
		})
		return
	}
	ref.Target = target
}

func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

func containsInline(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == "inline" {
			return true
		}
	}
	return false
}
