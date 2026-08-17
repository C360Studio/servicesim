package testkit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/stretchr/testify/require"
)

// TestKeptAliasDocsInlineEveryField pins Phase 10 unit 4's inline-field-list
// requirement (docs/proposals/framework-seam.md: the "Kept" alias list's
// doc comments are rewritten to inline the field list, because "go doc
// cannot follow an alias to an internal type"). It reads server.go's AST
// rather than a golden copy of the field list, so a field added to the
// internal type without a matching doc update fails here instead of
// silently drifting out of what `go doc ./testkit <Alias>` can show a
// consumer outside this module.
func TestKeptAliasDocsInlineEveryField(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, parser.ParseComments)
	require.NoError(t, err, "parsing testkit/server.go")

	docFor := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			doc := gd.Doc
			if ts.Doc != nil {
				doc = ts.Doc
			}
			if doc != nil {
				docFor[ts.Name.Name] = doc.Text()
			}
		}
	}

	structCases := []struct {
		alias  string
		sample any
	}{
		{"Entry", journal.Entry{}},
		{"Stats", journal.Stats{}},
		{"Outcome", journal.Outcome{}},
		{"AuthObservation", journal.AuthObservation{}},
		{"StreamOutcome", journal.StreamOutcome{}},
		{"StreamClose", journal.StreamClose{}},
		{"Job", jobs.Job{}},
		{"JobStats", jobs.Stats{}},
	}
	for _, c := range structCases {
		t.Run(c.alias, func(t *testing.T) {
			t.Parallel()

			doc, ok := docFor[c.alias]
			require.True(t, ok, "no doc comment found on type %s in server.go", c.alias)

			typ := reflect.TypeOf(c.sample)
			for i := range typ.NumField() {
				name := typ.Field(i).Name
				require.Contains(t, doc, name,
					"%s's doc comment does not inline field %s — go doc %s would not show it", c.alias, name, c.alias)
			}
		})
	}

	interfaceCases := []struct {
		alias string
		typ   reflect.Type
	}{
		{"Journal", reflect.TypeOf((*journal.Journal)(nil)).Elem()},
		{"Jobs", reflect.TypeOf((*jobs.Store)(nil)).Elem()},
	}
	for _, c := range interfaceCases {
		t.Run(c.alias, func(t *testing.T) {
			t.Parallel()

			doc, ok := docFor[c.alias]
			require.True(t, ok, "no doc comment found on type %s in server.go", c.alias)

			for i := range c.typ.NumMethod() {
				name := c.typ.Method(i).Name
				require.Contains(t, doc, name,
					"%s's doc comment does not name method %s", c.alias, name)
			}
		})
	}
}
