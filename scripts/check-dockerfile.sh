#!/usr/bin/env bash
# Recurrence guard for Docker builds.
#
# `ARG FOO` with no default is unset unless `--build-arg FOO=` is passed.
# A `RUN set -u` (or `set -eux`) that expands `${FOO}` then dies with:
#   /bin/sh: 1: FOO: parameter not set
# That is exactly how the GitHub docker job failed on MIHOMO_ASSET.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
file="$root/Dockerfile"
if [[ ! -f "$file" ]]; then
  echo "missing Dockerfile" >&2
  exit 1
fi

python3 - "$file" <<'PY'
import re, sys

text = open(sys.argv[1], encoding="utf-8").read()
# Strip comments
lines = []
for line in text.splitlines():
    if line.strip().startswith("#"):
        continue
    lines.append(line)
body = "\n".join(lines)

args = {}
for m in re.finditer(r"(?im)^ARG\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*=\s*(.*))?$", body):
    name, default = m.group(1), m.group(2)
    args[name] = default is not None

# Join continuation RUN blocks
runs = []
cur = []
for line in body.splitlines():
    if cur:
        cur.append(line)
        if not line.rstrip().endswith("\\"):
            runs.append("\n".join(cur))
            cur = []
        continue
    if re.match(r"(?i)^RUN\b", line):
        if line.rstrip().endswith("\\"):
            cur = [line]
        else:
            runs.append(line)

failed = False
for run in runs:
    if not re.search(r"\bset\s+-[a-zA-Z]*u", run):
        continue
    for name, has_default in args.items():
        if has_default:
            continue
        # Safe expansions: ${NAME:-} ${NAME- } ${NAME:+} ${NAME+}
        if re.search(r"\$\{" + name + r"[:\+\?-]", run):
            continue
        if re.search(r"\$\{" + name + r"\}|\$" + name + r"\b", run):
            print(
                f"Dockerfile: RUN uses set -u and expands ${{{name}}} "
                f"but ARG {name} has no default. Use ARG {name}=\"\" or ${{{name}:-}}.",
                file=sys.stderr,
            )
            failed = True

if failed:
    sys.exit(1)
print("Dockerfile nounset check passed")
PY
