package testkit

import (
	"bytes"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/internal/paidhosts"
)

// allowLiveHostMarker is the same escape hatch scripts/lint-no-live-hosts.sh
// honours: a line carrying it is never reported, however many of hosts it
// names.
const allowLiveHostMarker = "servicesim:allow-live-host"

// AssertNoLiveHosts fails tb if any file under fsys — Go source included —
// names a real vendor hostname: hosts, plus the framework's own base list of
// paid hosts (internal/paidhosts.Base — api.openai.com among them, refused // servicesim:allow-live-host
// whether or not any registered profile names it), matched anywhere in a
// line and not only after a URL scheme, because a bare Go default
// ("api.acme.com") is exactly the footgun this guard exists for.
//
// It mirrors scripts/lint-no-live-hosts.sh's rules exactly: a line ending in
// the "servicesim:allow-live-host" marker is exempt, a file that looks
// binary (any NUL byte) is skipped the way grep -I skips it, and every path
// in skip — and everything under it — is not scanned at all. Pass your
// contracts directory in skip, the same way the script exempts one: its
// provenance record names the vendor's real documentation URL, which is not
// the mistake this guard exists to catch. ".git" is always skipped, whether
// or not the caller names it: a commit message or a reflog entry naming a
// paid host is not scenario, fixture or Go-source data this guard is for,
// and unlike a caller's own directories, no caller can meaningfully opt out
// of having one.
//
// The two-line idiom a consumer's CI runs:
//
//	set, _ := provider.NewSet(myProfiles()...)
//	testkit.AssertNoLiveHosts(t, os.DirFS("."), []string{"myprofile/contracts"}, set.LiveHosts()...)
//
// os.DirFS(".") over the module root is deliberate, not incidental: a base
// URL default lives in Go source, not in a scenario fixture, and this
// function's whole reason to exist is scanning that source, the same way
// lint-no-live-hosts.sh's own SEARCH_PATHS do today. A real module root
// commonly has more than a contracts directory that legitimately names a
// real host, though: documentation that cites a vendor's own docs URL is
// the same class of citation a contracts provenance record makes, so a
// caller whose docs do that should list them in skip too, exactly as
// lint-no-live-hosts.sh exempts docs/ by never walking it at all.
func AssertNoLiveHosts(tb testing.TB, fsys fs.FS, skip []string, hosts ...string) {
	tb.Helper()

	pattern, ok := liveHostPattern(hosts)
	if !ok {
		return // no host named at all — nothing to guard against
	}

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			tb.Errorf("testkit.AssertNoLiveHosts: walking %s: %v", path, err)
			return nil
		}
		if path == ".git" || skippedPath(path, skip) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		data, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			tb.Errorf("testkit.AssertNoLiveHosts: reading %s: %v", path, rerr)
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil // looks binary; grep -I would skip it too
		}

		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, allowLiveHostMarker) {
				continue
			}
			if pattern.MatchString(line) {
				tb.Errorf("testkit.AssertNoLiveHosts: %s:%d: real provider hostname: %s",
					path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		tb.Errorf("testkit.AssertNoLiveHosts: walking %s: %v", ".", err)
	}
}

// liveHostPattern builds the base-list-∪-hosts regexp AssertNoLiveHosts scans
// with. ok is false only when there is nothing to build — every host named
// (base or caller-supplied) was empty.
func liveHostPattern(hosts []string) (_ *regexp.Regexp, ok bool) {
	all := make([]string, 0, len(paidhosts.Base)+len(hosts))
	all = append(all, paidhosts.Base...)
	all = append(all, hosts...)

	var quoted []string
	for _, h := range all {
		if h == "" {
			continue
		}
		quoted = append(quoted, regexp.QuoteMeta(h))
	}
	if len(quoted) == 0 {
		return nil, false
	}
	return regexp.MustCompile(strings.Join(quoted, "|")), true
}

// skippedPath reports whether path is one of skip's entries, or lives under
// one of them.
func skippedPath(path string, skip []string) bool {
	for _, s := range skip {
		s = strings.TrimSuffix(s, "/")
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}
