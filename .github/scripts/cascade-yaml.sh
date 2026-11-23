#!/usr/bin/env bash
# After a core release, makes the yaml module require the core version that was released.
#
#   TAG=v0.1.0 bash .github/scripts/cascade-yaml.sh
#
# Run from the root of a checkout. It records the tag in .release/versions.yaml (through
# set-core-version.sh), makes config/yaml require that version of the core, and runs go mod
# tidy there, which is what puts the checksums in go.sum. Nothing else is touched, and a
# workflow opens a pull request with what changed.
#
# The tag is chosen by whoever creates the release, so it is read from the environment,
# refused unless it is a plain version before anything is changed, and only then given to
# go mod edit, where a tag such as "v1.0.0 -replace=..." would otherwise be an option. A tag
# of v2 or later is refused too: the core's module path has no /v2, so Go could not use it.
#
# The Go proxy takes a few seconds to learn of a new tag, so tidy is tried again, a bounded
# number of times (CASCADE_RETRIES, at most 10; CASCADE_WAIT seconds apart, at most 60). If
# it never works, the files are put back as they were.
set -euo pipefail

# Character ranges mean letters of the current language in some locales.
export LC_ALL=C

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

tag="${TAG:-}"
versions=".release/versions.yaml"
module="config/yaml"

version='^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'

if [[ ${#tag} -gt 100 || ! $tag =~ $version ]]; then
	echo "refusing to use the tag ${tag@Q}: it is not a version such as v0.1.0 or v1.2.3-rc.1 (v2 and later are not possible: the module path has no /v2)" | head -c 300 >&2
	echo >&2

	exit 1
fi

if [[ ! -f $module/go.mod ]]; then
	echo "$module/go.mod does not exist: run this from the root of the checkout" >&2

	exit 1
fi

# A number from the environment, or the default, within 1 and max.
bounded() {
	local value="$1" default="$2" max="$3"

	if [[ ! $value =~ ^[0-9]{1,9}$ ]]; then
		value="$default"
	fi

	((value < 1)) && value=1
	((value > max)) && value="$max"

	echo "$value"
}

retries="$(bounded "${CASCADE_RETRIES:-}" 5 10)"
wait="$(bounded "${CASCADE_WAIT:-}" 15 60)"

# What it changes is put back if it does not work, so a failed run leaves nothing half done.
backup="$(mktemp -d)"
done_ok=0
had_sum=0

[[ -f $module/go.sum ]] && had_sum=1

restore() {
	if [[ $done_ok != 1 ]]; then
		cp "$backup/go.mod" "$module/go.mod" 2>/dev/null || true
		cp "$backup/versions.yaml" "$versions" 2>/dev/null || true

		if [[ $had_sum == 1 ]]; then
			cp "$backup/go.sum" "$module/go.sum" 2>/dev/null || true
		else
			rm -f "$module/go.sum"
		fi
	fi

	rm -rf "$backup"
}

trap restore EXIT

cp "$module/go.mod" "$backup/go.mod"
[[ -f $versions ]] && cp "$versions" "$backup/versions.yaml"
[[ $had_sum == 1 ]] && cp "$module/go.sum" "$backup/go.sum"

TAG="$tag" bash "$here/set-core-version.sh" "$versions"

# The workspace of a developer's machine is not part of this: the module is tidied on its own.
export GOWORK=off

(cd "$module" && go mod edit -require="go.krak3n.dev/foundation@$tag")

attempt=1

while true; do
	if (cd "$module" && go mod tidy); then
		break
	fi

	if ((attempt >= retries)); then
		echo "go mod tidy failed $attempt times: the Go proxy may not have $tag yet, or it is not a release of the core" >&2

		exit 1
	fi

	echo "go mod tidy failed (attempt $attempt of $retries): trying again in ${wait}s" >&2

	sleep "$wait"

	attempt=$((attempt + 1))
done

done_ok=1

echo "$module requires go.krak3n.dev/foundation $tag"
