#!/usr/bin/env bash
# Tests for set-core-version.sh. Run: bash .github/scripts/test-set-core-version.sh
#
# The script puts a release tag into a file that a workflow then uses, and the tag
# is chosen by whoever creates the release, so the tests are mostly about what it
# refuses: anything that isn't a plain version, including shell and sed syntax.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/set-core-version.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# fixture FILE [CORE_VERSION] writes a versions file.
fixture() {
	cat >"$1" <<YAML
module-sets:
  core:
    version: ${2:-v0.0.0}
    modules:
      - go.krak3n.dev/foundation
  otel:
    version: v0.0.0
    modules:
      - go.krak3n.dev/foundation/telemetry/opentelemetry
excluded-modules: []
YAML
}

failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# version_of FILE SET prints the version of a module set.
version_of() {
	awk -v set="$2" '
		/^  [A-Za-z0-9_-]+:/ { in_set = ($1 == set ":") }
		in_set && /^    version:/ { print $2; exit }
	' "$1"
}

accepts() {
	local tag="$1" file="$work/versions.yaml"

	fixture "$file"

	if ! (cd "$work" && TAG="$tag" bash "$script" "$file" >/dev/null 2>&1); then
		fail "should accept $(printf %q "$tag")"

		return
	fi

	[ "$(version_of "$file" core)" = "$tag" ] || fail "$tag: core version is $(version_of "$file" core)"
	[ "$(version_of "$file" otel)" = "v0.0.0" ] || fail "$tag: the otel version was changed to $(version_of "$file" otel)"

	# Nothing else in the file changed.
	local want="$work/want.yaml"

	fixture "$want" "$tag"

	diff -q "$file" "$want" >/dev/null 2>&1 || fail "$tag: more than the core version changed"
}

rejects() {
	local tag="$1" file="$work/versions.yaml"

	fixture "$file"
	cp "$file" "$work/before.yaml"

	rm -f "$work/PWNED" PWNED

	if (cd "$work" && TAG="$tag" bash "$script" "$file" >/dev/null 2>&1); then
		fail "should reject $(printf %q "$tag")"
	fi

	cmp -s "$file" "$work/before.yaml" || fail "$(printf %q "$tag"): the file was changed although the tag was refused"

	[ ! -e "$work/PWNED" ] && [ ! -e PWNED ] || fail "$(printf %q "$tag"): a command in the tag was run"
}

for tag in v0.1.0 v1.2.3 v10.20.30 v0.0.1 v0.1.0-rc.1 v1.0.0-beta v2.0.0-alpha.1.2; do
	accepts "$tag"
done

for tag in \
	"" "0.1.0" "v1.0" "v1" "v01.0.0" "v1.00.0" "v1.0.0+build" "v1.0.0 " " v1.0.0" $'v1.0.0\n' $'v1.0.0\nversion: v9.9.9' \
	"v1.0.0-" "v1.0.0-rc..1" "v1.0.0-rc." "vv1.0.0" "V1.0.0" 'v1.0.0\' "v1.0.0/../x" "v1.0.0'" 'v1.0.0"' \
	'v1.0.0$(touch PWNED)' 'v1.0.0`touch PWNED`' 'v1.0.0"; touch PWNED; "' 'v1.0.0;touch PWNED' 'v1.0.0|touch PWNED' \
	'v1.0.0&&touch PWNED' 'v1.0.0$IFS' 'v1.0.0${HOME}' 'v1.0.0-$(touch PWNED)' 'v1.0.0-rc.1/s/x/y/' 'v1.0.0&' \
	'../v1.0.0' 'telemetry/opentelemetry/v1.0.0'; do
	rejects "$tag"
done

# It takes the tag from the environment, not from its arguments, so a tag can't
# be mistaken for the name of the file to edit.
fixture "$work/versions.yaml"

if (cd "$work" && bash "$script" "v9.9.9" >/dev/null 2>&1); then
	fail "a tag given as an argument with no TAG set should not be accepted"
fi

# A file with no core section is an error, and is left as it is.
printf 'module-sets:\n  otel:\n    version: v0.0.0\n' >"$work/nocore.yaml"
cp "$work/nocore.yaml" "$work/nocore.before"

if (cd "$work" && TAG=v1.0.0 bash "$script" "$work/nocore.yaml" >/dev/null 2>&1); then
	fail "a file with no core module set should be an error"
fi

cmp -s "$work/nocore.yaml" "$work/nocore.before" || fail "a file with no core module set was changed"

# A missing file is an error.
if (cd "$work" && TAG=v1.0.0 bash "$script" "$work/missing.yaml" >/dev/null 2>&1); then
	fail "a missing file should be an error"
fi

if [ "$failures" -ne 0 ]; then
	echo "$failures check(s) failed" >&2

	exit 1
fi

echo "ok"
