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
# URLs, and the contracts/ goldens carry a provenance comment naming the
# documentation page they were verified against.
#
# Suppress a deliberate occurrence with a trailing `servicesim:allow-live-host`
# comment on the same line.

set -euo pipefail

cd "$(dirname "$0")/.."

# Hostnames that would cause a real, billable request if a base URL leaked.
PATTERN='(api\.exa\.ai|exa\.ai|api\.tavily\.com|tavily\.com|api\.perplexity\.ai|perplexity\.ai|api\.openai\.com)'

SEARCH_PATHS=(scenarios provider scenario testkit internal cmd)

status=0
for path in "${SEARCH_PATHS[@]}"; do
  [ -d "$path" ] || continue

  # -I skips binaries. Exclude Go doc comments is NOT desirable — a real
  # hostname in a code comment is still a footgun for the next reader who
  # copies it into a default.
  while IFS= read -r line; do
    case "$line" in
      *servicesim:allow-live-host*) continue ;;
    esac
    echo "FAIL: real provider hostname in test data: $line"
    status=1
  done < <(grep -rInE "$PATTERN" "$path" || true)
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
