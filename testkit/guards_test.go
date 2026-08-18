package testkit_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/c360studio/servicesim/testkit"
)

// TestAssertNoLiveHosts_FailsOnRealHostInGoSource is the footgun
// lint-no-live-hosts.sh exists for: a real vendor hostname sitting in a Go
// default, not only in fixture data.
func TestAssertNoLiveHosts_FailsOnRealHostInGoSource(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		// Deliberately un-reserved: the fixture this test proves AssertNoLiveHosts fails on.
		"client.go": {Data: []byte(`const baseURL = "https://api.openai.com/v1"` + "\n")}, // servicesim:allow-live-host
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil)
	if !stub.Failed() {
		t.Fatal("want AssertNoLiveHosts to fail on a real hostname in a Go file")
	}
	if !strings.Contains(stub.Message(), "api.openai.com") { // servicesim:allow-live-host -- asserting the failure message names it.
		t.Fatalf("failure message = %q, want it to name the offending host", stub.Message())
	}
}

// TestAssertNoLiveHosts_FailsOnRealHostInFixture proves the guard scans
// fixture data too, not only *.go files.
func TestAssertNoLiveHosts_FailsOnRealHostInFixture(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		// Deliberately un-reserved: the fixture this test proves AssertNoLiveHosts fails on.
		"scenarios/happy.yaml": {Data: []byte("url: https://exa.ai/search\n")}, // servicesim:allow-live-host
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil)
	if !stub.Failed() {
		t.Fatal("want AssertNoLiveHosts to fail on a real hostname in a fixture")
	}
}

// TestAssertNoLiveHosts_CallerSuppliedHostIsCaught proves a hostname a
// caller passes through the hosts variadic — an out-of-tree profile's own
// Profile.Hosts, via Set.CredentialNames' sibling Set.LiveHosts — is
// refused on the same terms as the framework's own base list.
func TestAssertNoLiveHosts_CallerSuppliedHostIsCaught(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"client.go": {Data: []byte(`const baseURL = "https://api.acme.com/v1"` + "\n")},
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil, "api.acme.com")
	if !stub.Failed() {
		t.Fatal("want AssertNoLiveHosts to fail on a caller-supplied host")
	}
}

// TestAssertNoLiveHosts_SkippedDirDoesNotFail proves the skip list works:
// a real host inside a directory the caller named (a contracts bundle,
// whose provenance names the vendor's real documentation URL) must not
// fail the scan.
func TestAssertNoLiveHosts_SkippedDirDoesNotFail(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		// Deliberate: this is the "inside a skipped dir" fixture.
		"acme/contracts/README.md": {Data: []byte("verified against https://api.openai.com/docs\n")}, // servicesim:allow-live-host
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, []string{"acme/contracts"})
	if stub.Failed() {
		t.Fatalf("want no failure for a host inside a skipped directory, got: %s", stub.Message())
	}
}

// TestAssertNoLiveHosts_EscapeHatchSuppressesTheLine proves the
// servicesim:allow-live-host marker works exactly as it does for
// scripts/lint-no-live-hosts.sh: a deliberate occurrence on the marked line
// is not reported, but an unmarked occurrence still is.
func TestAssertNoLiveHosts_EscapeHatchSuppressesTheLine(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"client.go": {Data: []byte(strings.Join([]string{
			`// example only: api.openai.com servicesim:allow-live-host`,
			`const baseURL = "https://api.openai.com/v1"`, // deliberately unmarked INSIDE the fixture; outer guard: servicesim:allow-live-host
			``,
		}, "\n"))},
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil)
	if !stub.Failed() {
		t.Fatal("want the unmarked line to still fail")
	}
	if strings.Count(stub.Message(), "testkit.AssertNoLiveHosts") != 1 {
		t.Fatalf("want exactly one reported line (the unmarked one), got: %s", stub.Message())
	}
}

// TestAssertNoLiveHosts_GitDirIsAlwaysSkipped proves ".git" is exempt without
// the caller having to name it: a commit message or reflog entry naming a
// paid host is not scenario, fixture or Go-source data, and — unlike a
// caller's own directories — nothing a caller writes can opt it out.
func TestAssertNoLiveHosts_GitDirIsAlwaysSkipped(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		// Deliberate: this is the "inside .git" fixture. servicesim:allow-live-host
		".git/COMMIT_EDITMSG": {Data: []byte("fix: point base URL back at api.openai.com\n")}, // servicesim:allow-live-host
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil)
	if stub.Failed() {
		t.Fatalf("want no failure for a host inside .git, got: %s", stub.Message())
	}
}

// TestAssertNoLiveHosts_PassesOnReservedHosts is the boring, necessary
// positive case: reserved-domain fixtures and Go source must not fail.
func TestAssertNoLiveHosts_PassesOnReservedHosts(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"client.go":            {Data: []byte(`const baseURL = "https://acme.test/v1"` + "\n")},
		"scenarios/happy.yaml": {Data: []byte("url: https://source.example/doc\n")},
	}

	stub := &stubTB{}
	testkit.AssertNoLiveHosts(stub, fsys, nil)
	if stub.Failed() {
		t.Fatalf("want no failure on reserved-domain data, got: %s", stub.Message())
	}
}
