#!/usr/bin/env bash
# audit_frontend_imports.sh — machine-check the module-import hard constraint
# (governance v1, architecture ruling annotation #2): every frontend module
# imports ONLY core.js; cross-domain interaction goes through DOM events,
# never sibling imports. Exit 1 on any violation.
#
# Rules:
#   static/core.js        — the foundation; may not import any sibling.
#   static/app.js         — the entry; may import core.js only.
#   static/<domain>.js    — future S2 modules; may import core.js only.
#   static/i18n.js        — classic script (globals), must stay import-free.
#   vendored libs (vis-network.min.js etc.) — skipped by allowlist.
set -u
cd "$(dirname "$0")/.." || exit 1
STATIC=internal/server/static
SKIP_RE='^(i18n|vis-network\.min)\.js$'

status=0
for f in "$STATIC"/*.js; do
  base=$(basename "$f")
  [[ "$base" =~ $SKIP_RE ]] && continue
  # Collect static import specifiers (import ... from "...").
  grep -oE 'from[[:space:]]+"[^"]+"' "$f" 2>/dev/null | sed -E 's/from[[:space:]]+"([^"]+)"/\1/' | while read -r spec; do
    case "$spec" in
      ./core.js|"core.js") : ;; # the one allowed dependency
      *) echo "VIOLATION: $base imports \"$spec\" (only ./core.js is allowed)"; exit 1 ;;
    esac
  done || status=1
  # Dynamic import() must also target core only.
  if grep -nE 'import\(["'"'"'][^)"'"'"']+["'"'"']\)' "$f" | grep -v './core.js' | grep -q .; then
    echo "VIOLATION: $base uses dynamic import() beyond core.js"
    status=1
  fi
done

# i18n.js must remain a classic script: no module syntax at all.
if grep -qE '^[[:space:]]*(import|export)[[:space:]]' "$STATIC/i18n.js" 2>/dev/null; then
  echo "VIOLATION: i18n.js must stay a classic script (no import/export)"
  status=1
fi

if [ "$status" = 0 ]; then
  echo "IMPORT AUDIT: PASS (modules depend only on core.js)"
else
  echo "IMPORT AUDIT: FAIL"
fi
exit $status
