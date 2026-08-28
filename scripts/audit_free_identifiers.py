#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""audit_free_identifiers.py — machine gate for cross-module closure leaks.

The import audit (audit_frontend_imports.sh) proves modules import only
core.js, but v0.6.16 taught us the runtime blind spot: a moved block can
still reference a variable that stayed behind in another module's closure
(badgeSeq / userPrefs / subsCache class) — ReferenceError only when the
code path runs.

Discriminator (precise, low-noise): flag any identifier that is
  FREE in module A  (referenced, not defined in A, not a core export,
                     not a known browser/JS builtin)
  and DEFINED in module B != A  (as function/let/const/var)
That signature = a cross-module closure leak; single-module free ids
(property names, strings) stay silent.

Zero-build constraint untouched: dev-side tool only. Exit 1 on any hit.
Gate rule (alice, v0.6.17 order): this check must pass before assembly.
"""
import io
import os
import re
import sys

STATIC = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                      "internal", "server", "static")
SKIP_RE = re.compile(r"^(i18n|vis-network\.min)\.js$")

BUILTIN = set("""window document console JSON Math Array Object String Number Boolean Date
Promise Set Map RegExp Error EvalError RangeError TypeError URIError parseInt parseFloat
isNaN isFinite encodeURIComponent decodeURIComponent setTimeout clearTimeout setInterval
clearInterval fetch URL URLSearchParams FormData FileReader CustomEvent Event Image
Audio Blob File navigator location localStorage sessionStorage history screen vis
I18N undefined null true false this self arguments
of in new typeof void delete do else for if return switch try catch finally while with
var let const function class extends super import export default async await
break continue case throw instanceof yield""".split())
MEMBER_NOISE = set("""length push pop slice splice indexOf includes forEach map filter join
concat reverse sort keys values entries then catch apply call bind toString toFixed
toLowerCase toUpperCase trim split replace test exec charAt startsWith endsWith repeat
abs ceil floor round min max pow sqrt random now getTime toLocaleString createElement
createTextNode appendChild insertBefore removeChild remove append prepend closest
querySelector querySelectorAll getAttribute setAttribute removeAttribute addEventListener
removeEventListener dispatchEvent classList dataset style textContent innerHTML value
checked focus blur click submit preventDefault stopPropagation contains getBoundingClientRect
getItem setItem removeItem stringify parse apply assign defineProperty
name message stack code href type id kind role label title className target currentTarget
detail address subject body to cc value key data result error status text json blob
arrayBuffer headers ok url method credentials mode cache signal""".split())

# Heuristic-noise baseline (reviewed 2026-08-26, v0.6.17): short locals the
# regex def-extraction cannot always attribute (anonymous callbacks, tight
# loops) plus a few cross-file coincidental name collisions. Verified to
# contain NO actual leaks: the v0.6.16 breakage (badgeSeq/userPrefs/
# setInboxBadge/subsCache/systemDomain/attachment trio) all stay OUTSIDE
# this set and are caught. Extend ONLY after human review of the new hit.
# 2026-08-27 car3 review (Felix): +days/folder/i/o — def-extraction blind
# spots, NOT leaks: `days` = graphPrefs object key + windowLabel(days)
# named-fn param (params of NAMED functions aren't captured); `folder` =
# event-detail object key; `i`/`o` = i18n placeholder-var object keys.
NOISE_BASELINE = set("""account active add btn c card cls dir domain el f files fn idx
k loadAudit loadInbox me msg n panel path pick prefs preview pv s sel tab ts toggle
unread v days folder i o""".split())


def strip_noise(src):
    src = re.sub(r"//[^\n]*", "", src)
    src = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
    src = re.sub(r'"(?:[^"\\]|\\.)*"', '""', src)
    src = re.sub(r"'(?:[^'\\]|\\.)*'", "''", src)
    src = re.sub(r"`(?:[^`\\]|\\.)*`", "``", src)
    return src


def defs(code):
    d = set(re.findall(r"\b(?:async\s+)?function\s*\*?\s+(\w+)", code))
    d |= set(re.findall(r"\b(?:const|let|var)\s+(\w+)", code))
    for m in re.findall(r"\b(?:const|let|var)\s*\{([^}]+)\}", code):
        d |= {p.strip().split(":")[0].strip() for p in m.split(",") if p.strip()}
    for m in re.findall(r"(?:async\s+)?\(([^()]*)\)\s*=>", code):
        d |= {p.strip() for p in m.split(",") if p.strip()}
    for m in re.findall(r"\bfunction\s*\(([^)]*)\)", code):
        d |= {p.strip() for p in m.split(",") if p.strip()}
    d |= set(re.findall(r"\bcatch\s*\(\s*(\w+)\s*\)", code))
    return d


def defs_exported(code):
    """B-side signature set: only names that plausibly live at module scope
    (function names + simple var/let/const declarations). Params and
    destructuring are NOT module-scope exports."""
    d = set(re.findall(r"\b(?:async\s+)?function\s*\*?\s+(\w+)", code))
    d |= set(re.findall(r"\b(?:const|let|var)\s+(\w+)", code))
    return d


def used(code):
    # The lookbehind kills property accesses (x.msg) and name tails.
    return set(re.findall(r"(?<![.\w$])([A-Za-z_$][\w$]*)", code))


def main():
    core = io.open(os.path.join(STATIC, "core.js"), encoding="utf-8").read()
    core_exports = set(re.findall(r"export (?:async )?function (\w+)", core))
    core_exports |= set(re.findall(r"export (?:const|let|var) (\w+)", core_exports and core))

    modules = {}
    for base in sorted(os.listdir(STATIC)):
        if not base.endswith(".js") or SKIP_RE.match(base):
            continue
        code = strip_noise(io.open(os.path.join(STATIC, base), encoding="utf-8").read())
        modules[base] = (defs(code), defs_exported(code), used(code))

    hits = []
    for base, (defined, _dexp, used_ids) in modules.items():
        free = used_ids - defined - core_exports - BUILTIN - MEMBER_NOISE - NOISE_BASELINE
        for ident in sorted(free):
            for other, (_od, odef, _ou) in modules.items():
                if other != base and ident in odef:
                    hits.append("%s references '%s' defined in %s (cross-module closure leak)"
                                % (base, ident, other))
    # Third leak class (P0 01M0ZMAC): a module CALLS a core export it never
    # imports. The free-identifier pass can't see it (core exports are
    # exempt), and it only blows up at runtime ("basicAuth is not defined").
    # core.js itself is the definition site; wizard.js is a classic script
    # with its own local copies, so locally-defined names are fine.
    for base, (defined, _dx, used_ids) in modules.items():
        if base == "core.js":
            continue
        raw = io.open(os.path.join(STATIC, base), encoding="utf-8").read()
        m = re.search(r"import \{([^}]+)\} from [\"']./core\.js[\"']", raw)
        if not m:
            # Classic scripts (wizard.js) carry their own local copies of
            # these helpers and are not part of the ESM graph.
            continue
        imported = set(x.strip() for x in m.group(1).split(","))
        called = set(re.findall(r"(?<![.\w$])([A-Za-z_$][\w$]*)\s*\(", raw))
        for ident in sorted((called & core_exports) - imported - defined):
            hits.append("%s calls core export '%s' without importing it" % (base, ident))
    if hits:
        print("FREE-IDENTIFIER AUDIT: FAIL")
        for h in hits:
            print("  " + h)
        return 1
    print("FREE-IDENTIFIER AUDIT: PASS (no cross-module closure leaks)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
