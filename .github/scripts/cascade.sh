#!/usr/bin/env bash
# After a core release, makes the modules that depend on it require the core version that
# was released.
#
#   TAG=v0.1.0 MODULES="config/yaml config/etcd" bash .github/scripts/cascade.sh
#
# Run from the root of a checkout. It records the tag in .release/versions.yaml (through
# set-core-version.sh), makes each module in MODULES require that version of the core and
# runs go mod tidy there, which is what puts the checksums in go.sum. Nothing else is
# touched, and a workflow opens a pull request with what changed. The modules are listed in
# the Makefile (MODULES) and the workflow gives the same list: a test keeps them equal.
#
# The tag is chosen by whoever creates the release, so it is read from the environment,
# refused unless it is a plain version before anything is changed, and only then given to
# go mod edit, where a tag such as "v1.0.0 -replace=..." would otherwise be an option. A tag
# of v2 or later is refused too: the core's module path has no /v2, so Go could not use it.
# The module list is refused unless it is plain too (directories below the checkout, named
# in lower case, no links, each with a go.mod, no more than 16, none twice), because each
# becomes a path that is entered and a go.mod that is rewritten.
#
# The Go proxy takes a few seconds to learn of a new tag, so tidy is tried again, a bounded
# number of times for each module (CASCADE_RETRIES, at most 10; CASCADE_WAIT seconds apart,
# at most 60). It is all or nothing: if any module can't be moved, every file is put back as
# it was.
set -euo pipefail

# Character ranges mean letters of the current language in some locales.
export LC_ALL=C

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

tag="${TAG:-}"
versions=".release/versions.yaml"

version='^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$'

if [[ ${#tag} -gt 100 || ! $tag =~ $version ]]; then
	echo "refusing to use the tag ${tag@Q}: it is not a version such as v0.1.0 or v1.2.3-rc.1 (v2 and later are not possible: the module path has no /v2)" | head -c 300 >&2
	echo >&2

	exit 1
fi

# refuse REASON prints a short reason, which never holds more of the list than a name, and stops.
refuse() {
	echo "refusing the modules: $1" | head -c 300 >&2
	echo >&2

	exit 1
}

list="${MODULES:-}"
name='[a-z][a-z0-9_-]*(/[a-z][a-z0-9_-]*)*'

if [[ ${#list} -gt 1000 || ! $list =~ ^$name( $name)*$ ]]; then
	refuse "MODULES must be directories below the checkout, lower case, one space apart"
fi

read -r -a modules <<<"$list"

if ((${#modules[@]} > 16)); then
	refuse "more than 16"
fi

seen=" "

for module in "${modules[@]}"; do
	if [[ $seen == *" $module "* ]]; then
		refuse "a module is given twice"
	fi

	seen+="$module "

	# No part of the path is a link, so the module is inside the checkout.
	path=""

	IFS=/ read -r -a parts <<<"$module"

	for part in "${parts[@]}"; do
		path+="${path:+/}$part"

		if [[ -L $path ]]; then
			refuse "${module:0:60} goes through a link"
		fi
	done

	if [[ ! -d $module || ! -f $module/go.mod || -L $module/go.mod ]]; then
		refuse "${module:0:60} has no go.mod: run this from the root of the checkout"
	fi
done

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

restore() {
	if [[ $done_ok != 1 ]]; then
		cp "$backup/versions.yaml" "$versions" 2>/dev/null || true

		local i=0

		for module in "${modules[@]}"; do
			cp "$backup/$i/go.mod" "$module/go.mod" 2>/dev/null || true

			if [[ -f $backup/$i/go.sum ]]; then
				cp "$backup/$i/go.sum" "$module/go.sum" 2>/dev/null || true
			else
				rm -f "$module/go.sum"
			fi

			i=$((i + 1))
		done
	fi

	rm -rf "$backup"
}

trap restore EXIT

[[ -f $versions ]] && cp "$versions" "$backup/versions.yaml"

i=0

for module in "${modules[@]}"; do
	mkdir "$backup/$i"
	cp "$module/go.mod" "$backup/$i/go.mod"
	[[ -f $module/go.sum ]] && cp "$module/go.sum" "$backup/$i/go.sum"

	i=$((i + 1))
done

TAG="$tag" bash "$here/set-core-version.sh" "$versions"

# The workspace of a developer's machine is not part of this: each module is tidied on its own.
export GOWORK=off

for module in "${modules[@]}"; do
	(cd "$module" && go mod edit -require="go.krak3n.dev/foundation@$tag")

	attempt=1

	while true; do
		if (cd "$module" && go mod tidy); then
			break
		fi

		if ((attempt >= retries)); then
			echo "go mod tidy failed $attempt times in $module: the Go proxy may not have $tag yet, or it is not a release of the core" >&2

			exit 1
		fi

		echo "go mod tidy failed in $module (attempt $attempt of $retries): trying again in ${wait}s" >&2

		sleep "$wait"

		attempt=$((attempt + 1))
	done

	echo "$module requires go.krak3n.dev/foundation $tag"
done

done_ok=1
