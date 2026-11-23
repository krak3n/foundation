#!/usr/bin/env bash
# Sets the version of the "core" module set in .release/versions.yaml to a release tag.
#
#   TAG=v1.2.3 .github/scripts/set-core-version.sh [versions.yaml]
#
# The tag comes from a release, whose name is chosen by whoever creates it, and git
# allows $, (, ), backticks and quotes in tag names. So it is read from the environment,
# never pasted into a script, and refused unless it is a plain semantic version: after
# that it contains nothing the shell or awk would treat as syntax.
set -euo pipefail

tag="${TAG:-}"
file="${1:-.release/versions.yaml}"

version='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'

if [[ ! $tag =~ $version ]]; then
	echo "refusing to use the tag ${tag@Q}: it is not a version such as v1.2.3 or v1.2.3-rc.1" >&2

	exit 1
fi

if [[ ! -f $file ]]; then
	echo "$file does not exist" >&2

	exit 1
fi

tmp="$(mktemp "${file}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

# Change the version line of the core module set, and only that one.
awk -v tag="$tag" '
	/^  [A-Za-z0-9_-]+:/ { in_core = ($1 == "core:") }
	in_core && !done && /^    version:/ { print "    version: " tag; done = 1; next }
	{ print }
	END { if (!done) exit 3 }
' "$file" >"$tmp" || {
	echo "$file has no core module set with a version" >&2

	exit 1
}

# Keep the file's permissions, then replace it in one step.
chmod --reference="$file" "$tmp" 2>/dev/null || chmod "$(stat -f '%Lp' "$file" 2>/dev/null || echo 644)" "$tmp"

mv "$tmp" "$file"
trap - EXIT
