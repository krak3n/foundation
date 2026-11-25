#!/usr/bin/env python3
"""Checks that every go.mod asks for the oldest Go version the project supports.

    GOVERSION=$(go env GOVERSION) python3 .github/scripts/check_go_versions.py [--root DIR]

The `go` line of a module is the least version that can build it. The project supports
the current stable release and the one before it, so the line is the first release of the
one before (`go 1.26.0`, the form `go mod init` writes, so a dependency that says `1.26.0`
never moves it): lower promises a version CI doesn't test, and higher stops the older
release building the module at all (a toolchain would be downloaded in its place, so a
test of the older release would be a test of the newer one). The bare minor (`go 1.26`)
sorts before `1.26.0` and is refused, so there is one form.

The current stable release comes from the environment (`GOVERSION`, as `go env GOVERSION`
prints it), so nothing is fetched and nothing needs editing when Go ships: the check
goes red, and `make go-version` is the fix. A go.mod comes from a pull request, so it is
read as text and never run, links are not followed, a file that isn't a regular file is
refused without being opened, and anything that can't be understood fails.

Exit status: 0 all fine, 1 a module is wrong, 2 the check couldn't be made.
"""

import os
import pathlib
import re
import stat
import sys
import unicodedata

# Bounds, so that a hostile tree can't make the check run long or hide a module.
MAX_BYTES = 1 << 20
MAX_MODULES = 64
MAX_DEPTH = 40

SKIPPED = {"testdata", "vendor", "node_modules"}

RELEASE = re.compile(r"go1\.(0|[1-9][0-9]{0,3})(\.(0|[1-9][0-9]{0,3}))?", re.ASCII)

BOM = "﻿"


class VersionError(ValueError):
    """The Go version could not be understood."""


class TreeError(Exception):
    """The modules could not be listed."""


def oldstable(goversion: str) -> str:
    """The minor before the release `goversion` names, as "1.26" for "go1.27.3"."""
    m = RELEASE.fullmatch(goversion)
    if m is None:
        raise VersionError("GOVERSION is not a release such as go1.27.3")

    minor = int(m.group(1))
    if minor < 1:
        raise VersionError("GOVERSION has no release before it")

    return f"1.{minor - 1}"


def shown(value: str) -> str:
    """Text from a file, made safe and short enough to put in a message."""
    return ascii(value[:24]) + ("..." if len(value) > 24 else "")


def check_bytes(data: bytes, name: str, want: str) -> list[str]:
    if len(data) > MAX_BYTES:
        return [f"{name}: larger than {MAX_BYTES} bytes"]

    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        return [f"{name}: not valid UTF-8"]

    return check_text(text, name, want)


def check_text(text: str, name: str, want: str) -> list[str]:
    found: list[str] = []

    if text.startswith(BOM):
        found.append(f"{name}:1: starts with a byte order mark")
        text = text[1:]

    accepted = re.compile(re.escape(want) + r"\.0", re.ASCII)

    directives = 0
    in_block = False

    for number, line in enumerate(re.split(r"\r?\n", text), start=1):
        if any(c not in "\t\r" and unicodedata.category(c) in ("Cc", "Cf", "Zl", "Zp") for c in line):
            found.append(f"{name}:{number}: has a control or format character")

            continue

        code = line.split("//", 1)[0].strip(" \t\r")
        words = code.split()

        if in_block:
            if code == ")":
                in_block = False

            continue

        if code.endswith("("):
            in_block = True

            continue

        if not words:
            continue

        if words[0] == "toolchain":
            found.append(f"{name}:{number}: has a toolchain line, which would replace the Go that CI runs")
        elif words[0] == "go":
            directives += 1

            if directives > 1:
                found.append(f"{name}:{number}: go directive given more than once")
            elif len(words) != 2 or not accepted.fullmatch(words[1]):
                got = words[1] if len(words) == 2 else code
                found.append(f"{name}:{number}: go directive is {shown(got)}, want {want}.0")

    if directives == 0:
        found.append(f"{name}: no go directive")

    return found


def find_modules(root: pathlib.Path) -> list[pathlib.Path]:
    """Every go.mod under root, sorted by path. Links are not followed."""
    root = pathlib.Path(root)

    if not root.is_dir():
        raise TreeError("the root is not a directory")

    modules: list[pathlib.Path] = []

    def walk(directory: pathlib.Path, depth: int) -> None:
        if depth > MAX_DEPTH:
            raise TreeError(f"the tree is more than {MAX_DEPTH} directories deep")

        with os.scandir(directory) as entries:
            children = sorted(entries, key=lambda e: e.name)

        for entry in children:
            if entry.is_symlink():
                continue

            if entry.is_dir(follow_symlinks=False):
                if entry.name in SKIPPED or entry.name.startswith("."):
                    continue

                walk(pathlib.Path(entry.path), depth + 1)
            elif entry.name == "go.mod":
                modules.append(pathlib.Path(entry.path))

                if len(modules) > MAX_MODULES:
                    raise TreeError(f"more than {MAX_MODULES} modules")

    try:
        walk(root, 0)
    except OSError:
        raise TreeError("a directory could not be read") from None

    return sorted(modules, key=lambda p: p.relative_to(root).as_posix())


def check_file(path: pathlib.Path, name: str, want: str) -> list[str]:
    try:
        # lstat, so that nothing is opened that isn't a plain file: a FIFO would block.
        if not stat.S_ISREG(path.lstat().st_mode):
            return [f"{name}: is not a regular file"]

        with open(path, "rb") as f:
            data = f.read(MAX_BYTES + 1)
    except OSError:
        return [f"{name}: could not be read"]

    return check_bytes(data, name, want)


def main(argv: list[str], env: dict[str, str]) -> int:
    root = None
    args = list(argv)

    while args:
        arg = args.pop(0)

        if arg == "--root" and args and root is None:
            root = args.pop(0)
        else:
            print("usage: check_go_versions.py [--root DIR]", file=sys.stderr)

            return 2

    if "GOVERSION" not in env:
        print("check_go_versions: GOVERSION is not set (use `go env GOVERSION`)", file=sys.stderr)

        return 2

    try:
        want = oldstable(env["GOVERSION"])
        base = pathlib.Path(root if root is not None else ".")
        modules = find_modules(base)
    except (VersionError, TreeError) as err:
        print(f"check_go_versions: {err}", file=sys.stderr)

        return 2

    if not modules:
        print("check_go_versions: no go.mod found", file=sys.stderr)

        return 2

    found: list[str] = []

    for path in modules:
        found.extend(check_file(path, path.relative_to(base).as_posix(), want))

    if found:
        print("\n".join(found), file=sys.stderr)
        print(f"every go.mod must say go {want}.0, the release before the current stable: make go-version GO={want}", file=sys.stderr)

        return 1

    print(f"{len(modules)} modules, all at go {want}.0")

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:], dict(os.environ)))
