#!/usr/bin/env bash
# Tests for cascade.sh. Run: bash .github/scripts/test-cascade.sh
#
# After a core release the modules that depend on it (config/yaml, config/etcd, the ones
# the MODULES variable lists) have to require the new core version. The script does it
# with go mod edit and go mod tidy, in a repository checkout, on a tag that is chosen by
# whoever creates the release, so the tests are about four things: it does exactly that
# to the modules it is given and nothing else, all or nothing (a module that can't be
# tidied leaves every other one as it was), it refuses a tag or a module list that isn't
# plain before it touches anything (either would otherwise reach go mod edit or a path),
# and it is bounded when the Go proxy has not got the tag yet (it takes a few seconds
# after a tag is pushed). The proxy is a directory of files, so nothing here uses the
# network.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="${CASCADE_SCRIPT:-$here/cascade.sh}"
real_go="$(command -v go)"

work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT

failures=0

# Every "should refuse" below would pass for a script that is not there.
[ -f "$script" ] || {
	echo "FAIL: $script does not exist" >&2

	exit 1
}

fail() {
	echo "FAIL: $*" >&2
	failures=$((failures + 1))
}

# repo DIR makes the checkout the script runs in: the core module at its root, the
# yaml and etcd modules in config/ (which need a core that can't be fetched yet), a
# module the script is not given (tools), and the versions file.
repo() {
	local dir="$1"

	mkdir -p "$dir/.release" "$dir/config/yaml" "$dir/config/etcd" "$dir/tools"

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
  etcd:
    version: v0.0.0
    modules:
      - go.krak3n.dev/foundation/config/etcd
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

	cat >"$dir/config/etcd/go.mod" <<'MOD'
module go.krak3n.dev/foundation/config/etcd

go 1.26

require go.krak3n.dev/foundation v0.0.0
MOD

	cat >"$dir/config/etcd/etcd.go" <<'GO'
package etcd

import _ "go.krak3n.dev/foundation/config"
GO

	# Not a module the cascade is told about: it must come out as it went in.
	cat >"$dir/tools/go.mod" <<'MOD'
module example.com/tools

go 1.26

require go.krak3n.dev/foundation v0.0.0
MOD

	cat >"$dir/tools/tools.go" <<'GO'
package tools

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

	# A sleep that takes no time and records what it was asked for, so the waits are
	# checked by their length and not by waiting.
	cat >"$1/sleep" <<SH
#!/usr/bin/env bash
echo "\$1" >>"$work/slept"
SH

	chmod +x "$1/sleep"
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
			TAG="$tag" MODULES="config/yaml config/etcd" \
			GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-modcacherw \
			GOMODCACHE="$work/modcache" \
			"$@" timeout 30 bash "$script"
	) >"$work/out" 2>&1
}

# required DIR [MODULE] prints the core version that MODULE (config/yaml by default) requires.
required() {
	awk '$1 == "go.krak3n.dev/foundation" { print $2 }' <(sed -n 's/^require //p;s/^\t//p' "$1/${2:-config/yaml}/go.mod") | head -1
}

version_of() {
	awk -v set="$2" '
		/^  [A-Za-z0-9_-]+:/ { in_set = ($1 == set ":") }
		in_set && /^    version:/ { print $2; exit }
	' "$1/.release/versions.yaml"
}

PROXY="$work/proxy"

# --- A plain version: every module in the list requires the new core, and only that changes ---

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
	[ "$(required "$work/r" config/etcd)" = "$tag" ] || fail "$tag: config/etcd requires $(required "$work/r" config/etcd)"
	[ "$(version_of "$work/r" core)" = "$tag" ] || fail "$tag: the core set is at $(version_of "$work/r" core)"
	[ "$(version_of "$work/r" yaml)" = "v0.0.0" ] || fail "$tag: the yaml set was changed to $(version_of "$work/r" yaml)"
	[ "$(version_of "$work/r" etcd)" = "v0.0.0" ] || fail "$tag: the etcd set was changed to $(version_of "$work/r" etcd)"
	[ -s "$work/r/config/yaml/go.sum" ] || fail "$tag: config/yaml/go.sum has nothing in it"
	[ -s "$work/r/config/etcd/go.sum" ] || fail "$tag: config/etcd/go.sum has nothing in it"

	# A module that is not in the list is left alone, though it requires the core too.
	[ "$(required "$work/r" tools)" = "v0.0.0" ] || fail "$tag: tools was moved to $(required "$work/r" tools)"
	cmp -s "$work/r/tools/go.mod" "$work/before/tools/go.mod" || fail "$tag: tools/go.mod changed"
	[ ! -e "$work/r/tools/go.sum" ] || fail "$tag: tools/go.sum was written"

	# Nothing else changed: not the core's go.mod, not the code, nothing new.
	cmp -s "$work/r/go.mod" "$work/before/go.mod" || fail "$tag: the core go.mod changed"
	cmp -s "$work/r/config/yaml/yaml.go" "$work/before/config/yaml/yaml.go" || fail "$tag: yaml.go changed"
	cmp -s "$work/r/config/etcd/etcd.go" "$work/before/config/etcd/etcd.go" || fail "$tag: etcd.go changed"

	extra="$(diff -rq "$work/before" "$work/r" | grep -v 'versions.yaml\|config/yaml/go.mod\|config/etcd/go.mod\|go.sum' || true)"
	[ -z "$extra" ] || fail "$tag: other files changed: $extra"

	# It leaves a module that builds.
	for module in config/yaml config/etcd; do
		(cd "$work/r/$module" && env GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-modcacherw GOMODCACHE="$work/modcache" "$real_go" build ./... >/dev/null 2>&1) ||
			fail "$tag: $module does not build against it"
	done

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

# --- A retraction in a module's go.mod is kept ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
printf '\nretract v0.0.1 // kept by the cascade\n' >>"$work/r/config/yaml/go.mod"
printf '\nretract v0.0.2 // kept by the cascade\n' >>"$work/r/config/etcd/go.mod"
stage "$PROXY" v0.1.0

run "$work/r" v0.1.0 || fail "a go.mod with a retraction was refused or failed: $(cat "$work/out")"

grep -q '^retract v0.0.1 // kept by the cascade$' "$work/r/config/yaml/go.mod" || fail "the retraction did not survive: $(cat "$work/r/config/yaml/go.mod")"
grep -q '^retract v0.0.2 // kept by the cascade$' "$work/r/config/etcd/go.mod" || fail "the retraction did not survive: $(cat "$work/r/config/etcd/go.mod")"
[ "$(required "$work/r")" = "v0.1.0" ] || fail "with a retraction, config/yaml requires $(required "$work/r")"

# --- Only the modules it is given are moved ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0

run "$work/r" v0.1.0 MODULES="config/etcd" || fail "one module was refused or failed: $(cat "$work/out")"

[ "$(required "$work/r" config/etcd)" = "v0.1.0" ] || fail "config/etcd requires $(required "$work/r" config/etcd)"
[ "$(required "$work/r")" = "v0.0.0" ] || fail "config/yaml was moved although it was not listed: $(required "$work/r")"

# --- All or nothing: a module that can't be tidied puts every module back ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0

# The second module needs a module that no proxy has, after the first has been moved.
printf '\nrequire example.com/missing v1.0.0\n' >>"$work/r/config/etcd/go.mod"
printf 'package etcd\n\nimport (\n\t_ "example.com/missing"\n\t_ "go.krak3n.dev/foundation/config"\n)\n' >"$work/r/config/etcd/etcd.go"
tree "$work/r" >"$work/before"

run "$work/r" v0.1.0 CASCADE_RETRIES=1 CASCADE_WAIT=0 && fail "should fail when one module can't be tidied"

tree "$work/r" >"$work/after"
cmp -s "$work/before" "$work/after" || fail "a failure in the second module left the first moved, or something else changed"
[ ! -e "$work/r/config/yaml/go.sum" ] || fail "config/yaml/go.sum was left behind"

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

	# It was the script that refused, in words (${tag@Q} is not in every bash).
	grep -q '^refusing' "$work/out" || fail "$(printf %q "${tag:0:40}"): no refusal in the output: $(head -c 200 "$work/out")"
done

# --- A module list that is not plain is refused before anything changes ---

modules_hostile=(
	''
	' '
	'.'
	'./'
	'..'
	'../x'
	'../../etc'
	'/etc'
	'/abs/path'
	'config/yaml/'
	'config//yaml'
	'config/./yaml'
	'config/../config/yaml'
	'./config/yaml'
	'-x'
	'--help'
	'config/yaml -x'
	'config/yaml;touch PWNED'
	'config/yaml$(touch PWNED)'
	'config/yaml`touch PWNED`'
	'config/yaml"'
	"config/yaml'"
	'config/yaml|id'
	'config/yaml&id'
	'config/yaml>PWNED'
	'config/yaml*'
	'config/*'
	'config/yaml?'
	'~'
	$'config/yaml\nconfig/etcd'
	$'config/yaml\tconfig/etcd'
	$'config/yaml\r'
	'config/yaml  config/etcd'
	' config/yaml'
	'config/yaml '
	'config/yaml config/yaml'
	'config/yaml config/etcd config/yaml'
	'config'
	'nope/missing'
	'Config/Yaml'
	'config/é'
	'link'
	'link/etcd'
	'.release'
	"$(printf 'a%0300d' 0)"
	"$(printf 'm%d ' $(seq 1 17))"
)

for list in "${modules_hostile[@]}"; do
	rm -rf "$PROXY" "$work/r" PWNED
	repo "$work/r"
	ln -s config/yaml "$work/r/link"
	stage "$PROXY" v0.1.0
	tree "$work/r" >"$work/before"

	if run "$work/r" v0.1.0 CASCADE_WAIT=0 MODULES="$list"; then
		fail "should refuse the modules $(printf %q "${list:0:40}")"
	fi

	tree "$work/r" >"$work/after"
	cmp -s "$work/before" "$work/after" || fail "$(printf %q "${list:0:40}"): files changed although the list was refused"

	[ ! -e "$work/r/PWNED" ] && [ ! -e PWNED ] || fail "$(printf %q "${list:0:40}"): it ran what was in the list"

	[ "$(wc -c <"$work/out")" -lt 600 ] || fail "$(printf %q "${list:0:40}"): a reason of $(wc -c <"$work/out") bytes"

	# It was the script that refused, not something else failing: it says so.
	grep -q '^refusing' "$work/out" || fail "$(printf %q "${list:0:40}"): no refusal in the output: $(head -c 200 "$work/out")"
done

# --- A link where the script writes is refused too, so nothing is written through it ---

for victim in config/etcd/go.sum config/yaml/go.sum .release/versions.yaml; do
	rm -rf "$PROXY" "$work/r" "$work/outside"
	repo "$work/r"
	stage "$PROXY" v0.1.0
	printf 'outside\n' >"$work/outside"
	rm -f "$work/r/$victim"
	ln -s "$work/outside" "$work/r/$victim"
	tree "$work/r" >"$work/before"

	if run "$work/r" v0.1.0 CASCADE_WAIT=0; then
		fail "$victim is a link: should refuse"
	fi

	tree "$work/r" >"$work/after"
	cmp -s "$work/before" "$work/after" || fail "$victim is a link: files changed"
	[ "$(cat "$work/outside")" = outside ] || fail "$victim is a link: it was written through"
	grep -q '^refusing' "$work/out" || fail "$victim is a link: no refusal in the output: $(head -c 200 "$work/out")"
done

# No list at all.
rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0

if (cd "$work/r" && env -u MODULES TAG=v0.1.0 GOPROXY="file://$PROXY" GOSUMDB=off GOWORK=off bash "$script" >/dev/null 2>&1); then
	fail "should refuse when MODULES is not set"
fi

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

	run "$work/r" "$tag" MODULES=config/yaml PATH="$work/bin:$PATH" PROXY="$PROXY" STAGED="$work/staged" APPEAR_AT="$appear" \
		CASCADE_RETRIES="$retries" CASCADE_WAIT="${LAG_WAIT:-0}"
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

# So is the wait between them, and a number with leading zeros is a number, not octal
# (bash reads 08 as an error and 010 as 8, which would have lifted both bounds).
waited() {
	rm -f "$work/slept"

	LAG_WAIT="$1" lag 9999 2 >/dev/null 2>&1 || true

	cat "$work/slept" 2>/dev/null | head -1
}

[ "$(waited 100000)" = 60 ] || fail "a huge wait was not cut to 60: $(waited 100000)"
[ "$(waited 080)" = 60 ] || fail "CASCADE_WAIT=080 was not cut to 60: $(waited 080)"
[ "$(waited 010)" = 10 ] || fail "CASCADE_WAIT=010 was not 10: $(waited 010)"
[ "$(waited 5)" = 5 ] || fail "CASCADE_WAIT=5 was not 5: $(waited 5)"
[ "$(waited 0)" = 1 ] || fail "CASCADE_WAIT=0 was not raised to 1: $(waited 0)"
[ "$(waited abc)" = 15 ] || fail "CASCADE_WAIT=abc was not the default 15: $(waited abc)"

# The number of tries, the same way.
tries() {
	lag 9999 "$1" >/dev/null 2>&1 || true

	cat "$work/count"
}

[ "$(tries 08)" = 8 ] || fail "CASCADE_RETRIES=08 gave $(tries 08) tries, want 8"
[ "$(tries 09)" = 9 ] || fail "CASCADE_RETRIES=09 gave $(tries 09) tries, want 9"
[ "$(tries 010)" = 10 ] || fail "CASCADE_RETRIES=010 gave $(tries 010) tries, want 10"
[ "$(tries 011)" = 10 ] || fail "CASCADE_RETRIES=011 gave $(tries 011) tries, want 10 at most"

# --- A checkout without one of the modules ---

rm -rf "$PROXY" "$work/r"
repo "$work/r"
stage "$PROXY" v0.1.0
rm -rf "$work/r/config/etcd"
tree "$work/r" >"$work/before"

run "$work/r" v0.1.0 && fail "should fail without config/etcd"
grep -q 'config/etcd' "$work/out" || fail "it does not say what is missing: $(cat "$work/out")"

tree "$work/r" >"$work/after"
cmp -s "$work/before" "$work/after" || fail "it changed files although config/etcd is missing"

if [ "$failures" -gt 0 ]; then
	echo "$failures failures" >&2

	exit 1
fi

echo "ok"
