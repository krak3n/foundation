#!/usr/bin/env bash
# Tests for cascade-yaml.sh. Run: bash .github/scripts/test-cascade-yaml.sh
#
# After a core release the yaml module has to require the new core version. The script
# does it with go mod edit and go mod tidy, in a repository checkout, on a tag that is
# chosen by whoever creates the release, so the tests are about three things: it does
# exactly that and nothing else, it refuses a tag that isn't a plain version before it
# touches anything (the tag would otherwise reach go mod edit as an option), and it is
# bounded when the Go proxy has not got the tag yet (it takes a few seconds after a tag
# is pushed). The proxy is a directory of files, so nothing here uses the network.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="${CASCADE_SCRIPT:-$here/cascade-yaml.sh}"
real_go="$(command -v go)"

work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT

failures=0

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# repo DIR makes the checkout the script runs in: the core module at its root, the
# yaml module in config/yaml (which needs a core that can't be fetched yet), and the
# versions file.
repo() {
	local dir="$1"

	mkdir -p "$dir/.release" "$dir/config/yaml"

	cat >"$dir/.release/versions.yaml" <<YAML
module-sets:
  core:
    version: v0.0.0
    modules:
      - go.krak3n.dev/foundation
  yaml:
    version: v0.0.0
    modules:
      - go.krak3n.dev/foundation/config/yaml
excluded-modules: []
YAML

	printf 'module go.krak3n.dev/foundation\n\ngo 1.26\n' >"$dir/go.mod"

	cat >"$dir/config/yaml/go.mod" <<'MOD'
module go.krak3n.dev/foundation/config/yaml

go 1.26

require go.krak3n.dev/foundation v0.0.0
MOD

	cat >"$dir/config/yaml/yaml.go" <<'GO'
package yaml

import _ "go.krak3n.dev/foundation/config"
GO
}

# stage DIR VERSION makes what the Go proxy serves for the core at VERSION.
stage() {
	local dir="$1" version="$2"
	local src="$work/src-$version"

	mkdir -p "$src/go.krak3n.dev/foundation@$version/config" "$dir/go.krak3n.dev/foundation/@v"

	printf 'module go.krak3n.dev/foundation\n\ngo 1.26\n' >"$src/go.krak3n.dev/foundation@$version/go.mod"
	printf 'package config\n' >"$src/go.krak3n.dev/foundation@$version/config/config.go"

	(cd "$src" && zip -qr "$dir/go.krak3n.dev/foundation/@v/$version.zip" "go.krak3n.dev/foundation@$version")

	cp "$src/go.krak3n.dev/foundation@$version/go.mod" "$dir/go.krak3n.dev/foundation/@v/$version.mod"
	printf '{"Version":"%s","Time":"2026-01-01T00:00:00Z"}' "$version" >"$dir/go.krak3n.dev/foundation/@v/$version.info"
	printf '%s\n' "$version" >>"$dir/go.krak3n.dev/foundation/@v/list"
}

# tree DIR prints a checksum of every file under it, so two states can be compared.
tree() {
	(cd "$1" && find . -type f | LC_ALL=C sort | while read -r f; do shasum "$f"; done)
}

# fake_go DIR COUNT makes a go that counts the runs of go mod tidy and, when APPEAR_AT
# is set, puts the staged proxy files in place on that run: the tag appearing late.
fake_go() {
	mkdir -p "$1"

	cat >"$1/go" <<SH
#!/usr/bin/env bash
if [ "\$1" = mod ] && [ "\$2" = tidy ]; then
	n=\$(cat "$2" 2>/dev/null || echo 0)
	n=\$((n + 1))
	echo \$n >"$2"

	if [ -n "\${APPEAR_AT:-}" ] && [ "\$n" -ge "\$APPEAR_AT" ]; then
		cp -R "\$STAGED/." "\$PROXY/"
	fi
fi

exec "$real_go" "\$@"
SH

	chmod +x "$1/go"
}

# run DIR TAG [VAR=value ...] runs the script in the checkout DIR, with the proxy and
# no network, and prints nothing; the status is the return value.
run() {
	local dir="$1" tag="$2"

	shift 2

	# Nothing is left over from an earlier run: a module that is in the cache needs no
	# proxy, which would hide a tag that is not there yet.
	chmod -R u+w "$work/modcache" 2>/dev/null
	rm -rf "$work/modcache"

	(
		cd "$dir" && env \
			TAG="$tag" \
			GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-modcacherw \
			GOMODCACHE="$work/modcache" \
			"$@" bash "$script"
	) >"$work/out" 2>&1
}

required() {
	awk '$1 == "go.krak3n.dev/foundation" { print $2 }' <(sed -n 's/^require //p;s/^\t//p' "$1/config/yaml/go.mod") | head -1
}

version_of() {
	awk -v set="$2" '
		/^  [A-Za-z0-9_-]+:/ { in_set = ($1 == set ":") }
		in_set && /^    version:/ { print $2; exit }
	' "$1/.release/versions.yaml"
}

PROXY="$work/proxy"

# --- A plain version: the yaml module requires the new core, and only that changes ---

for tag in v0.1.0 v1.2.3 v0.1.0-rc.1 v1.20.30; do
	rm -rf "$PROXY" "$work/r"
	repo "$work/r"
	stage "$PROXY" "$tag"
	cp -R "$work/r" "$work/before"

	if ! run "$work/r" "$tag"; then
		fail "$tag: refused, or failed: $(cat "$work/out")"
		rm -rf "$work/before"

		continue
	fi

	[ "$(required "$work/r")" = "$tag" ] || fail "$tag: config/yaml requires $(required "$work/r")"
	[ "$(version_of "$work/r" core)" = "$tag" ] || fail "$tag: the core set is at $(version_of "$work/r" core)"
	[ "$(version_of "$work/r" yaml)" = "v0.0.0" ] || fail "$tag: the yaml set was changed to $(version_of "$work/r" yaml)"
	[ -s "$work/r/config/yaml/go.sum" ] || fail "$tag: config/yaml/go.sum has nothing in it"

	# Nothing else changed: not the core's go.mod, not the code, nothing new.
	cmp -s "$work/r/go.mod" "$work/before/go.mod" || fail "$tag: the core go.mod changed"
	cmp -s "$work/r/config/yaml/yaml.go" "$work/before/config/yaml/yaml.go" || fail "$tag: yaml.go changed"

	extra="$(diff -rq "$work/before" "$work/r" | grep -v 'versions.yaml\|config/yaml/go.mod\|go.sum' || true)"
	[ -z "$extra" ] || fail "$tag: other files changed: $extra"

	# It leaves a module that builds.
	(cd "$work/r/config/yaml" && env GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-modcacherw GOMODCACHE="$work/modcache" "$real_go" build ./... >/dev/null 2>&1) ||
		fail "$tag: the yaml module does not build against it"

	rm -rf "$work/before"
done

# --- It can be run again with the same tag ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0

run "$work/r" v0.1.0 || fail "the first run failed: $(cat "$work/out")"
tree "$work/r" >"$work/first"
run "$work/r" v0.1.0 || fail "the second run failed: $(cat "$work/out")"
tree "$work/r" >"$work/second"
cmp -s "$work/first" "$work/second" || fail "running it twice changed things the second time"

# --- A tag that is not a plain version is refused before anything changes ---

hostile=(
	''
	'latest'
	'1.2.3'
	'v1'
	'v1.2'
	'v1.2.3.4'
	'v01.2.3'
	'v1.2.3+build'
	'v2.0.0'
	'v2.1.0-rc.1'
	'v10.0.0'
	'v1.2.3-'
	'v1.2.3-rc..1'
	'../../x'
	'v1.0.0 -replace=go.krak3n.dev/foundation=../evil'
	'v1.0.0 -require=evil.example/x@v1.0.0'
	'v1.0.0@x'
	'-v1.0.0'
	'--json'
	'v1.0.0;touch PWNED'
	'v1.0.0$(touch PWNED)'
	'v1.0.0`touch PWNED`'
	'v1.0.0"'
	"v1.0.0'"
	'v1.0.0|id'
	'v1.0.0&id'
	'v1.0.0>PWNED'
	$'v1.0.0\nv2.0.0'
	$'v1.0.0\r'
	'v1.0.0 '
	' v1.0.0'
	'V1.0.0'
	'v1.0.0-é'
	"$(printf 'v1.0.0-%0300d' 0)"
)

for tag in "${hostile[@]}"; do
	rm -rf "$PROXY" "$work/r" PWNED "$work/r/PWNED"
	repo "$work/r"
	stage "$PROXY" v1.0.0
	tree "$work/r" >"$work/before"

	if run "$work/r" "$tag" CASCADE_WAIT=0; then
		fail "should refuse $(printf %q "${tag:0:40}")"
	fi

	tree "$work/r" >"$work/after"
	cmp -s "$work/before" "$work/after" || fail "$(printf %q "${tag:0:40}"): files changed although the tag was refused"

	[ ! -e "$work/r/PWNED" ] && [ ! -e PWNED ] || fail "$(printf %q "${tag:0:40}"): it ran what was in the tag"

	# The reason is a line or two, and does not run on with what it was given.
	[ "$(wc -c <"$work/out")" -lt 600 ] || fail "$(printf %q "${tag:0:40}"): a reason of $(wc -c <"$work/out") bytes"
done

# No tag at all.
rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v1.0.0

if (cd "$work/r" && env -u TAG GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off bash "$script" >/dev/null 2>&1); then
	fail "should refuse when TAG is not set"
fi

# --- The tag is not on the proxy yet: retried a few times, then it fails, bounded ---

fake_go "$work/bin" "$work/count"

lag() {
	local appear="$1" retries="$2" tag="${3:-v0.1.0}"

	rm -rf "$PROXY" "$work/r" "$work/staged" "$work/count"
	repo "$work/r"
	stage "$work/staged" "$tag"
	mkdir -p "$PROXY"
	tree "$work/r" >"$work/before"

	run "$work/r" "$tag" PATH="$work/bin:$PATH" PROXY="$PROXY" STAGED="$work/staged" APPEAR_AT="$appear" \
		CASCADE_RETRIES="$retries" CASCADE_WAIT=0
}

# It appears on the third try, within five.
lag 3 5 || fail "a tag that appears on the third try was not waited for: $(cat "$work/out")"
[ "$(cat "$work/count")" = 3 ] || fail "tidy ran $(cat "$work/count") times, want 3: it should stop when it works"
[ "$(required "$work/r")" = v0.1.0 ] || fail "after the wait, config/yaml requires $(required "$work/r")"

# It never appears.
if lag 99 4; then
	fail "should fail when the tag never appears"
fi

[ "$(cat "$work/count")" = 4 ] || fail "tidy ran $(cat "$work/count") times with 4 allowed"

tree "$work/r" >"$work/after"
cmp -s "$work/before" "$work/after" || fail "nothing should be left changed after a failure"
grep -qi 'tidy' "$work/out" || fail "the failure does not say what failed: $(cat "$work/out")"

# What is allowed is bounded, however much is asked for.
if lag 9999 100000; then
	fail "should fail when the tag never appears (large retry count)"
fi

[ "$(cat "$work/count")" -le 10 ] || fail "$(cat "$work/count") attempts: the number of tries is not bounded"

# So is the wait between them.
start=$SECONDS

lag 9999 3 >/dev/null 2>&1

(cd "$work/r" && env TAG=v0.1.0 CASCADE_RETRIES=2 CASCADE_WAIT=100000 PATH="$work/bin:$PATH" PROXY="$PROXY" STAGED="$work/staged" APPEAR_AT=1 \
	GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-modcacherw GOMODCACHE="$work/modcache" \
	timeout 20 bash "$script" >/dev/null 2>&1)

[ $((SECONDS - start)) -lt 20 ] || fail "a huge wait was honoured"

# --- A checkout without the yaml module ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0
rm -rf "$work/r/config"
tree "$work/r" >"$work/before"

run "$work/r" v0.1.0 && fail "should fail without config/yaml"
grep -q 'config/yaml' "$work/out" || fail "it does not say what is missing: $(cat "$work/out")"

tree "$work/r" >"$work/after"
cmp -s "$work/before" "$work/after" || fail "it changed files although config/yaml is missing"

if [ "$failures" -gt 0 ]; then
	echo "$failures failures" >&2

	exit 1
fi

echo "ok"
