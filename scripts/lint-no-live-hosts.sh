#!/usr/bin/env bash
#
# Guard: no real provider hostnames in scenario or fixture data.
#
# Servicesim exists so tests never touch a paid API. A scenario that carries a
# real vendor hostname is one copy-paste away from a consuming repo pointing a
# base URL at production, and a recorded-traffic fixture that was not sanitized
# is the most likely way that happens. Scenario and golden data may only
# reference RFC 2606 / RFC 6761 reserved names (.test, .example, .invalid,
# .localhost, example.com).
#
# Documentation is exempt: docs/ must be able to cite the vendors' real doc
# URLs, and a profile's contracts/ bundle (profiles/<name>/contracts/ —
# provenance.yaml and README.md) carries a provenance comment naming the
# documentation page it was verified against. Before Phase 10 unit 6, every
# profile's bundle lived under the repository-root contracts/ directory,
# which this guard never walked at all (SEARCH_PATHS below never named it) —
# an implicit exemption by omission. Genericising contracts moved each
# bundle to profiles/<name>/contracts/, INSIDE profiles/, which this guard
# DOES walk, so the exemption is now explicit and pattern-based below: any
# directory literally named "contracts", wherever it appears under a scanned
# path. Pattern-based, not a per-provider list, so a future profile's own
# contracts/ bundle is exempt automatically, the same way an out-of-tree
# adopter's would never be scanned by this in-tree script at all.
#
# Suppress a deliberate occurrence with a trailing `servicesim:allow-live-host`
# comment on the same line.

set -euo pipefail

cd "$(dirname "$0")/.."

# Hostnames that would cause a real, billable request if a base URL leaked.
#
# This base list is hand-kept and is NOT replaced by --print-hosts below: it
# names paid hosts the sem* workload cares about (api.openai.com among them)
# whether or not any registered profile names them yet, so a build that never
# added an OpenAI profile still guards against one leaking in by hand
# (docs/proposals/framework-seam.md, house rule 3: "a pattern generated
# purely from Profile.Hosts would delete the one vendor the sem* workload is
# about to need"). The effective pattern is this list UNION every registered
# profile's Profile.Hosts, read from the built binary's own --print-hosts —
# which is what makes a profile's Hosts travel into this guard automatically
# instead of relying on someone remembering to hand-edit the line below (the
# gap that let MCP join with no host entry here for two Phase 8 releases).
BASE_PATTERN='api\.exa\.ai|exa\.ai|api\.tavily\.com|tavily\.com|api\.perplexity\.ai|perplexity\.ai|api\.openai\.com'

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
bin="$tmp/servicesim"
go build -o "$bin" ./cmd/servicesim

registered_hosts=$("$bin" --print-hosts 2>/dev/null | sed 's/[.[\*^$]/\\&/g' | paste -sd '|' -)

PATTERN="(${BASE_PATTERN}"
[ -n "$registered_hosts" ] && PATTERN="${PATTERN}|${registered_hosts}"
PATTERN="${PATTERN})"

# The root package (servicesim.go, doc.go, servicesim_test.go — the
# composition root every consumer's main.go copies from) is scanned as
# explicit files, not a directory, because it lives at the repo root
# alongside non-Go top-level entries this guard has no business walking.
# Adding it here is not optional: it is exactly the file a consumer's own
# main.go is written by reading, so a live hostname surviving in it (in a
# default, a comment, an example) is a footgun for every adopter, not just
# an in-tree risk.
shopt -s nullglob
root_go_files=(./*.go)
shopt -u nullglob

SEARCH_PATHS=(scenarios provider profiles scenario testkit internal cmd examples "${root_go_files[@]}")

status=0
scan_list="$tmp/scan_files"
for path in "${SEARCH_PATHS[@]}"; do
  [ -e "$path" ] || continue

  # Pattern-based exemption: any directory literally named "contracts" is
  # pruned before grep ever sees it — a profile's own verified-contract
  # bundle, in this repository or an out-of-tree one, not test data this
  # guard is meant to police. find -prune, not grep --exclude-dir, because
  # the latter is a GNU extension this repository cannot assume every CI
  # and developer machine's grep provides.
  find "$path" -type d -name contracts -prune -o -type f -print >"$scan_list"

  # -I skips binaries. Exclude Go doc comments is NOT desirable — a real
  # hostname in a code comment is still a footgun for the next reader who
  # copies it into a default.
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    while IFS= read -r line; do
      case "$line" in
        *servicesim:allow-live-host*) continue ;;
      esac
      echo "FAIL: real provider hostname in test data: $line"
      status=1
    done < <(grep -HInE "$PATTERN" "$file" 2>/dev/null || true)
  done <"$scan_list"
done

if [ "$status" -ne 0 ]; then
  cat >&2 <<'EOF'

Scenario and fixture data must use reserved example domains
(example.test, example.com, *.invalid) so that no test can be
redirected to a paid provider by a misconfigured base URL.

If an occurrence is deliberate, append the marker comment:
    servicesim:allow-live-host
EOF
  exit 1
fi

echo "OK: no real provider hostnames in scenario or fixture data"
