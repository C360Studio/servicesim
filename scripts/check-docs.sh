#!/usr/bin/env bash
#
# Guard: every name the documentation uses actually exists.
#
# This repository has more prose than code, and prose rots silently. A flag that
# was renamed, a built-in scenario that was dropped, a route that moved, a
# testkit symbol that never shipped — none of those break a test, they only
# break the reader, and by the time a consumer notices they have already stopped
# trusting the surrounding paragraphs. This script turns that class of drift
# into a build failure.
#
# WHAT IT PROVES
#
#   1. Every `--flag` / `-flag` in the scanned docs appears in the binary's
#      --help output (the binary is built from source first).
#   2. Every `builtin:<name>` and every WithBuiltin("<name>") names a file in
#      scenarios/protocol/.
#   3. Every `METHOD /path` in the docs is registered by a provider's Routes()
#      or by the admin mux.
#   4. Every `testkit.X` / `provider.X` named in the docs is an exported symbol
#      of that package, per `go doc -all`.
#
# WHAT IT DOES NOT PROVE
#
#   It is a NAME checker, not a semantic one. It cannot tell you that a
#   documented default is still the real default, that a flag still does what
#   the paragraph says, that a scenario still produces the described behaviour,
#   or that an example would compile. A clean run means "every name exists",
#   which is strictly weaker than "the docs are correct". Do not read it as the
#   latter.
#
# It also only sees names it can recognise: single-letter flags, unqualified Go
# identifiers, and anything written without backticks-or-code shape are outside
# its reach. The per-check counts printed at the end exist so that a broken
# extractor — the classic failure of scripts like this, a regex that silently
# matches nothing — is visible as a suspiciously small number rather than as a
# green run. A category that extracts zero claims, or a reference set that comes
# back empty, exits 2 as a BROKEN GUARD: distinct from exit 1, which means the
# documentation is wrong. A guard that cannot fail is not a guard.
#
# Exit codes: 0 every name exists, 1 the documentation names something that does
# not, 2 the guard itself is broken and checked nothing.
#
# Usage: bash scripts/check-docs.sh   (from anywhere; it locates the repo root)

set -euo pipefail

cd "$(dirname "$0")/.."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Documents in scope. docs/design/ is deliberately excluded: it is the design of
# record, written ahead of the code, so it legitimately names things that do not
# exist yet. contracts/ is excluded because it documents the *vendors'* APIs,
# not this binary's surface.
DOC_GLOBS="README.md CONTRIBUTING.md docs/*.md docs/adr/*.md"

# Flags that appear in the docs but belong to other programs (go, docker, task,
# curl). They are counted and reported as ignored rather than silently dropped,
# so this list cannot quietly grow into a way of hiding a real miss.
THIRD_PARTY_FLAGS="race count run bench cover rm it detach build no-cache
env volume file list silent header data include location output"

fail_count=0
claim_count=0
failures="$tmp/failures"
: >"$failures"

# note records one unknown name against the file and line it was read from.
note() { # file line message
	printf '%s:%s: %s\n' "$1" "$2" "$3" >>"$failures"
	fail_count=$((fail_count + 1))
}

# require_nonempty makes an empty reference set fatal. A reference extractor
# that matches nothing would otherwise pass every claim.
require_nonempty() { # file description
	if [ ! -s "$1" ]; then
		printf 'BROKEN GUARD: extracted 0 %s — the extractor, not the docs, is wrong.\n' "$2" >&2
		exit 2
	fi
}

# ---------------------------------------------------------------------------
# Documents in scope
# ---------------------------------------------------------------------------

docs="$tmp/docs"
: >"$docs"
for glob in $DOC_GLOBS; do
	for f in $glob; do
		[ -f "$f" ] || continue
		echo "$f" >>"$docs"
	done
done
require_nonempty "$docs" "documentation files"
doc_count=$(wc -l <"$docs" | tr -d ' ')

# grep_docs prints file:line:match for one pattern across every scanned doc.
grep_docs() { # extended-regexp
	while IFS= read -r f; do
		grep -onE "$1" "$f" 2>/dev/null | sed "s|^|$f:|" || true
	done <"$docs"
}

# grep_docs_lines prints file:line:whole-line, for checks that need the context
# the match sits in.
grep_docs_lines() { # extended-regexp
	while IFS= read -r f; do
		grep -nE "$1" "$f" 2>/dev/null | sed "s|^|$f:|" || true
	done <"$docs"
}

# ---------------------------------------------------------------------------
# 1. CLI flags
# ---------------------------------------------------------------------------

bin="$tmp/servicesim"
go build -o "$bin" ./cmd/servicesim

known_flags="$tmp/known_flags"
{ "$bin" --help 2>&1 || true; } | { grep -E '^  -[A-Za-z]' || true; } |
	sed -E 's/^  -+([A-Za-z0-9][A-Za-z0-9-]*).*/\1/' | sort -u >"$known_flags"
require_nonempty "$known_flags" "flags from --help"

ignored_flags="$tmp/ignored_flags"
printf '%s\n' $THIRD_PARTY_FLAGS | sort -u >"$ignored_flags"

# A flag token is one or two dashes then two or more name characters, not
# preceded by a word character (which is what keeps "credential-shaped" and
# "read-only" out) and not preceded by a slash (URL paths).
flag_claims="$tmp/flag_claims"
grep_docs '(^|[^A-Za-z0-9_/-])--?[A-Za-z][A-Za-z0-9-]*[A-Za-z0-9]' |
	sed -E 's/^(.*):([0-9]+):[^-]?/\1:\2:/' >"$flag_claims"

flags_checked=0
flags_ignored=0
while IFS=: read -r file line tok; do
	[ -n "${tok:-}" ] || continue
	name=$(printf '%s' "$tok" | sed -E 's/^-+//')
	if grep -qxF "$name" "$ignored_flags"; then
		flags_ignored=$((flags_ignored + 1))
		continue
	fi
	flags_checked=$((flags_checked + 1))
	grep -qxF "$name" "$known_flags" ||
		note "$file" "$line" "unknown flag '$tok' — not in servicesim --help"
done <"$flag_claims"
claim_count=$((claim_count + flags_checked))

# ---------------------------------------------------------------------------
# 2. Built-in scenarios
# ---------------------------------------------------------------------------

known_builtins="$tmp/known_builtins"
{ ls scenarios/protocol/*.yaml 2>/dev/null || true; } |
	sed -E 's|.*/||; s|\.yaml$||' | sort -u >"$known_builtins"
require_nonempty "$known_builtins" "scenarios from scenarios/protocol/"

builtin_claims="$tmp/builtin_claims"
{
	grep_docs 'builtin:[A-Za-z0-9_-]+'
	grep_docs 'WithBuiltin\("[A-Za-z0-9_-]+"\)'
} >"$builtin_claims"

builtins_checked=0
while IFS=: read -r file line tok; do
	[ -n "${tok:-}" ] || continue
	name=$(printf '%s' "$tok" | sed -E 's/^builtin://; s/^WithBuiltin\("//; s/"\)$//')
	builtins_checked=$((builtins_checked + 1))
	grep -qxF "$name" "$known_builtins" ||
		note "$file" "$line" "unknown built-in scenario '$name' — no scenarios/protocol/$name.yaml"
done <"$builtin_claims"
claim_count=$((claim_count + builtins_checked))

# ---------------------------------------------------------------------------
# 3. Routes
# ---------------------------------------------------------------------------
#
# Provider patterns are read from the handler files that define Routes(); admin
# routes from the route(mux, ...) registrations. This is a grep for the literal,
# not proof the handler is reachable — an integration test proves that.

known_provider_routes="$tmp/known_provider_routes"
{ grep -hoE '"(GET|POST|PUT|PATCH|DELETE) /[^"]*"' provider/*/handler.go 2>/dev/null || true; } |
	tr -d '"' | sort -u >"$known_provider_routes"
require_nonempty "$known_provider_routes" "registered provider routes"

known_routes="$tmp/known_routes"
{
	cat "$known_provider_routes"
	awk '
		/route\(mux, "/ {
			path = $0
			sub(/.*route\(mux, "/, "", path)
			sub(/".*/, "", path)
			rest = $0
			while (match(rest, /http\.Method[A-Za-z]+/)) {
				# 11 = length("http.Method")
				print toupper(substr(rest, RSTART + 11, RLENGTH - 11)) " " path
				rest = substr(rest, RSTART + RLENGTH)
			}
		}
	' internal/admin/handler.go 2>/dev/null
} | sort -u >"$known_routes"
require_nonempty "$known_routes" "registered routes"

known_paths="$tmp/known_paths"
sed -E 's/^[A-Z]+ //' "$known_routes" | sort -u >"$known_paths"

# Routes are documented inside backticks. A query string is dropped, since
# `GET /__admin/requests?namespace=<name>` is a claim about the route.
#
# Method matching is strict only in a markdown table row, which is where this
# repository documents its listener surface. Elsewhere the prose deliberately
# shows methods that must NOT work — troubleshooting.md explains the 405 for
# `GET /search` — so outside a table only the path is required to exist.
route_claims="$tmp/route_claims"
grep_docs_lines '`(GET|POST|PUT|PATCH|DELETE) /' >"$route_claims"

routes_checked=0
routes_strict=0
while IFS=: read -r file line rest; do
	[ -n "${rest:-}" ] || continue
	strict=no
	case "$rest" in
	'|'*) strict=yes ;;
	esac
	for tok in $(printf '%s' "$rest" | grep -oE '`(GET|POST|PUT|PATCH|DELETE) /[A-Za-z0-9_/.{}<>-]*' | tr ' ' '~'); do
		route=$(printf '%s' "$tok" | tr -d '`' | tr '~' ' ')
		path=${route#* }
		routes_checked=$((routes_checked + 1))
		if [ "$strict" = yes ]; then
			routes_strict=$((routes_strict + 1))
			grep -qxF "$route" "$known_routes" ||
				note "$file" "$line" "unregistered route '$route' — no such method+path in Routes() or the admin mux"
		else
			grep -qxF "$path" "$known_paths" ||
				note "$file" "$line" "unregistered path '$path' — no such pattern in Routes() or the admin mux"
		fi
	done
done <"$route_claims"
claim_count=$((claim_count + routes_checked))

# ---------------------------------------------------------------------------
# 4. Exported Go symbols
# ---------------------------------------------------------------------------

known_symbols="$tmp/known_symbols"
for pkg in testkit provider; do
	# Declarations only. Prose is indented four spaces by go doc and never
	# reaches these patterns, which is what keeps a word in a doc comment from
	# passing as a symbol that exists.
	go doc -all "./$pkg" | awk -v p="$pkg" '
		function emit(n) { sub(/[^A-Za-z0-9_].*/, "", n); if (n ~ /^[A-Z]/) print p "." n }
		/^(const|var) \($/ { block = 1; next }
		block && /^\)/     { block = 0; next }
		block && /^\t[A-Z]/ { emit($1); next }
		/^func \(/         { emit($3); next }
		/^func /           { emit($2); next }
		/^type /           { emit($2); next }
		/^(const|var) /    { emit($2); next }
	'
done | sort -u >"$known_symbols"
require_nonempty "$known_symbols" "exported symbols from go doc"

symbol_claims="$tmp/symbol_claims"
grep_docs '\b(testkit|provider)\.[A-Z][A-Za-z0-9_]*' >"$symbol_claims"

symbols_checked=0
while IFS=: read -r file line tok; do
	[ -n "${tok:-}" ] || continue
	symbols_checked=$((symbols_checked + 1))
	grep -qxF "$tok" "$known_symbols" ||
		note "$file" "$line" "unknown symbol '$tok' — not exported by go doc ./${tok%%.*}"
done <"$symbol_claims"
claim_count=$((claim_count + symbols_checked))

# ---------------------------------------------------------------------------
# Published image references
# ---------------------------------------------------------------------------
#
# Every ghcr.io reference in the docs must actually resolve. This category was
# added after the failure it catches happened twice in one day: the README
# pointed at ghcr.io/c360studio/servicesim:v0.1.0 while nothing was published,
# and later while the publish pipeline had in fact tagged 0.1.0 — metadata-action
# strips the leading v — so the documented pin returned "manifest unknown" both
# times. A newcomer's first command failing is the most expensive documentation
# bug there is.
#
# ORDERING NOTE for a release: publish first, then update the docs. A commit
# that documents a tag before it exists will fail this check, which is the
# correct order of operations rather than a limitation to work around.
#
# Network failure is NOT a documentation failure. If the registry cannot be
# reached at all this skips loudly rather than failing, because a ghcr outage
# breaking an unrelated pull request is how a guard earns a reputation for
# noise and stops being read.

images_checked=0
images_placeholders=0
images_skipped=""

image_refs="$tmp/image_refs"
grep_docs 'ghcr\.io/[a-z0-9._/-]+[:@][A-Za-z0-9._:-]+' | sort -u >"$image_refs" || true

if [ -s "$image_refs" ]; then
	# One probe first, to tell "registry unreachable" from "tag absent".
	probe_repo="c360studio/servicesim"
	probe_token=$(curl -fsS --max-time 20 \
		"https://ghcr.io/token?scope=repository:${probe_repo}:pull&service=ghcr.io" 2>/dev/null |
		sed -n 's/.*"token":"\([^"]*\)".*/\1/p') || probe_token=""

	if [ -z "$probe_token" ]; then
		images_skipped="registry unreachable"
	else
		while IFS= read -r hit; do
			file=${hit%%:*}
			rest=${hit#*:}
			line=${rest%%:*}
			ref=${rest#*:}

			repo=${ref#ghcr.io/}
			case "$ref" in
			*@*)
				repo=${repo%@*}
				reference=${ref#*@}
				;;
			*)
				repo=$(echo "$repo" | sed 's/:[^:]*$//')
				reference=${ref##*:}
				;;
			esac

			# Documentation placeholders are not references. The plan
			# document writes ghcr.io/…:sha-<commit>, and the reference
			# pattern stops at the '<', leaving a stub like "sha-". Counted
			# rather than dropped, so this cannot quietly become a way for a
			# real tag to escape the check.
			case "$reference" in
			*- | *. | *:)
				images_placeholders=$((images_placeholders + 1))
				continue
				;;
			esac

			token=$(curl -fsS --max-time 20 \
				"https://ghcr.io/token?scope=repository:${repo}:pull&service=ghcr.io" 2>/dev/null |
				sed -n 's/.*"token":"\([^"]*\)".*/\1/p') || token=""
			[ -n "$token" ] || {
				images_skipped="registry unreachable"
				break
			}

			# NO -f here, deliberately. With -f curl exits non-zero on a 404,
			# which this loop would then read as a network failure and skip —
			# turning the one thing the check exists to catch, a documented tag
			# that was never published, into a silent pass. Without it the HTTP
			# status is reported and only a genuine transport failure yields 000.
			code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 -I \
				-H "Authorization: Bearer ${token}" \
				-H 'Accept: application/vnd.oci.image.index.v1+json' \
				-H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' \
				-H 'Accept: application/vnd.oci.image.manifest.v1+json' \
				"https://ghcr.io/v2/${repo}/manifests/${reference}" 2>/dev/null) || code="000"

			images_checked=$((images_checked + 1))
			case "$code" in
			200) ;;
			000) images_skipped="registry unreachable" ;;
			*) note "$file" "$line" "image reference '${ref}' does not resolve (HTTP ${code}) — publish it, or fix the tag" ;;
			esac
		done <"$image_refs"
	fi
fi

# ---------------------------------------------------------------------------
# 6. The contracts index table
# ---------------------------------------------------------------------------
#
# contracts/ is excluded from DOC_GLOBS because it documents the vendors' APIs.
# Its index table is the one exception: the "Base URL simulated" column is a
# claim about THIS binary, and it is the first page an adopter reads to decide
# what to trust.
#
# Checked in BOTH directions, because it has been wrong in both. It once claimed
# a route the binary did not serve, AND omitted Exa POST /answer plus two
# Perplexity routes whose goldens were already committed beside it. An adopter
# built a "servicesim is one-shot request/response only" inventory partly from
# this table, so an omission here is not cosmetic — it is a reader concluding a
# capability does not exist.
#
# The omission direction is the half a name checker cannot normally do: every
# other category proves documented names are real, while this one also proves
# real names are documented.
#
# Rows in the "NOT SIMULATED" table below it carry a bare path with no method
# (`/contents`), so requiring METHOD + path skips them without needing to parse
# table boundaries.

contracts_index="contracts/README.md"
contracts_checked=0

if [ -f "$contracts_index" ]; then
	claimed_routes="$tmp/claimed_routes"
	grep -oE '`(GET|POST|PUT|PATCH|DELETE) /[A-Za-z0-9_/.{}-]*`' "$contracts_index" 2>/dev/null |
		tr -d '`' | sort -u >"$claimed_routes" || true

	# Direction 1: everything the table claims is simulated must be registered.
	while IFS= read -r route; do
		[ -n "$route" ] || continue
		contracts_checked=$((contracts_checked + 1))
		grep -qxF "$route" "$known_provider_routes" || {
			line=$(grep -nF "\`$route\`" "$contracts_index" | head -1 | cut -d: -f1)
			note "$contracts_index" "${line:-1}" \
				"index table claims '$route' is simulated, but no provider registers it"
		}
	done <"$claimed_routes"

	# Direction 2: every registered provider route must appear in the table.
	while IFS= read -r route; do
		[ -n "$route" ] || continue
		contracts_checked=$((contracts_checked + 1))
		grep -qxF "$route" "$claimed_routes" ||
			note "$contracts_index" "1" \
				"index table omits '$route', which this build serves — a reader takes the table as the list of what is simulated"
	done <"$known_provider_routes"

	claim_count=$((claim_count + contracts_checked))
fi

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

printf 'docs guard: name existence only, %s document(s) scanned\n\n' "$doc_count"
printf '  flags     %4d claim(s) vs %s in --help (%d third-party ignored)\n' \
	"$flags_checked" "$(wc -l <"$known_flags" | tr -d ' ')" "$flags_ignored"
printf '  builtins  %4d claim(s) vs %s in scenarios/protocol/\n' \
	"$builtins_checked" "$(wc -l <"$known_builtins" | tr -d ' ')"
printf '  routes    %4d claim(s) vs %s registered pattern(s) (%d method-strict, in tables)\n' \
	"$routes_checked" "$(wc -l <"$known_routes" | tr -d ' ')" "$routes_strict"
printf '  symbols   %4d claim(s) vs %s exported name(s)\n' \
	"$symbols_checked" "$(wc -l <"$known_symbols" | tr -d ' ')"
printf '  contracts %4d claim(s) — index table vs %s provider route(s), both directions\n' \
	"$contracts_checked" "$(wc -l <"$known_provider_routes" | tr -d ' ')"
if [ -n "$images_skipped" ]; then
	printf '  images    SKIPPED (%s) — not a documentation failure\n\n' "$images_skipped"
else
	printf '  images    %4d reference(s) resolved against ghcr.io (%d placeholder(s) ignored)\n\n' \
		"$images_checked" "$images_placeholders"
fi

# Every category is expected to find claims in this repository's documentation:
# the README alone names flags, built-ins, routes and testkit symbols. A zero
# here therefore means the extractor stopped matching, not that the docs went
# quiet — and a silent zero is exactly how a guard like this rots into
# decoration. It is a hard failure, distinct from a documentation failure.
empty_categories=""
[ "$flags_checked" -gt 0 ] || empty_categories="$empty_categories flags"
[ "$builtins_checked" -gt 0 ] || empty_categories="$empty_categories builtins"
[ "$routes_checked" -gt 0 ] || empty_categories="$empty_categories routes"
[ "$symbols_checked" -gt 0 ] || empty_categories="$empty_categories symbols"
[ "$contracts_checked" -gt 0 ] || empty_categories="$empty_categories contracts"
if [ -n "$empty_categories" ]; then
	printf 'BROKEN GUARD: 0 claims extracted for:%s\n' "$empty_categories" >&2
	printf 'Nothing was checked there. Fix the extractor in %s.\n' "$0" >&2
	exit 2
fi

if [ "$fail_count" -ne 0 ]; then
	# Deduplicated, because one line repeating a name is one thing to fix.
	sort -u "$failures" >"$tmp/failures.sorted"
	echo "FAIL: $(wc -l <"$tmp/failures.sorted" | tr -d ' ') unknown name(s) in $claim_count claim(s) checked:" >&2
	echo >&2
	cat "$tmp/failures.sorted" >&2
	cat >&2 <<'EOF'

Either the documentation is stale, or the name was renamed and the docs were not
updated with it. Fix the document — do not relax this guard to match the prose.
EOF
	exit 1
fi

cat <<EOF
OK: $claim_count claim(s) checked, every name exists.

This proves only that the names are real. It does not prove the sentences around
them are true: defaults, behaviour, and whether an example compiles are all
outside what a name checker can see.
EOF
