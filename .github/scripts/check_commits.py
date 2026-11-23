#!/usr/bin/env python3
"""Checks that the commits of a pull request, and its title, are conventional commits.

    python3 .github/scripts/check_commits.py --base SHA --head SHA [--root DIR]

    PR_TITLE='feat: ...' python3 .github/scripts/check_commits.py --base SHA --head SHA

release-please writes the changelog and picks the version from the commit messages, so
each one must be `type(scope)!: subject`: a type and a scope from the lists below, a
lowercase subject (Dependabot's `chore(deps): Bump ...` is the one exception) with no full stop, and a header of at most 72 characters. The title is
held to the same form in case the pull request is squashed. A merge commit is refused:
rebase onto main instead.

Everything here is attacker-influenced text, since anyone who can open a pull request
chooses it. So a message is only read, never run or put in a command; the two ids are
checked to be full commit ids before git sees them (a branch name or an option is never
taken for one); git is run without a shell, with its output bounded; and what is said
about a commit is a fixed reason and the first 12 characters of its id, never any of its
text. Anything that can't be understood fails.

Exit status: 0 all fine, 1 a message is wrong, 2 the check couldn't be made.
"""

import os
import re
import subprocess
import sys
import threading
import unicodedata

TYPES = ("feat", "fix", "docs", "test", "refactor", "perf", "ci", "build", "chore", "revert", "style")
SCOPES = ("internal", "config", "watch", "interrupt", "http", "yaml", "examples", "deps")

# Bounds, so that a hostile history can't make the check run long or use much memory.
MAX_HEADER = 72
MAX_MESSAGE = 4096
MAX_COMMITS = 1000
MAX_OUTPUT = 16 << 20
GIT_TIMEOUT = 60

HEADER = re.compile(
    r"(?P<type>[A-Za-z]*)(?:\((?P<scope>[^()]*)\))?(?P<bang>!?): (?P<subject>.*)",
    re.DOTALL,
)

COMMIT_ID = re.compile(r"[0-9a-f]{40}|[0-9a-f]{64}", re.ASCII)


class CheckError(Exception):
    """The check could not be made. The text never repeats what the check was given."""


def unsafe(ch: str, allowed: str = "") -> bool:
    """A character that can hide or forge text: a control, format, separator (other than a
    plain space), surrogate, private use or unassigned character."""
    if ch in allowed:
        return False

    if ch == " ":
        return False

    category = unicodedata.category(ch)

    return category[0] in "CZ"


def check_header(header: str) -> list[str]:
    """The problems with a header (the first line of a message, or a title)."""
    if len(header) > MAX_HEADER:
        return [f"the header is longer than {MAX_HEADER} characters"]

    if any(unsafe(ch) for ch in header):
        return ["the header has a character that is not allowed (a control, format or separator character)"]

    match = HEADER.fullmatch(header)

    if match is None:
        return ["the header is not in the form type(scope): subject, with one space after the colon"]

    found = []

    if match["type"] not in TYPES:
        found.append("the type is not one of: " + ", ".join(TYPES))

    if match["scope"] is not None and match["scope"] not in SCOPES:
        found.append("the scope is not one of: " + ", ".join(SCOPES))

    subject = match["subject"]

    if subject == "":
        found.append("the subject is empty")
    elif subject != subject.strip():
        found.append("the header has a space at the start or end of the subject")
    else:
        # Dependabot writes "chore(deps): Bump ...", and that is the only place a capital is
        # allowed: its prefix is set in .github/dependabot.yml and the rest is its own.
        dependabot = match["type"] == "chore" and match["scope"] == "deps"

        if subject[0].isupper() and not dependabot:
            found.append("the subject must start in lowercase")

        if subject.endswith("."):
            found.append("the subject must not end with a full stop")

    return found


def check_title(title: str) -> list[str]:
    """The problems with a pull request title."""
    return check_header(title)


def check_message(message: str) -> list[str]:
    """The problems with a whole commit message: the header, then an optional body."""
    if len(message) > MAX_MESSAGE:
        return [f"the message is longer than {MAX_MESSAGE} bytes"]

    if len(message.encode("utf-8", "surrogatepass")) > MAX_MESSAGE:
        return [f"the message is longer than {MAX_MESSAGE} bytes"]

    if message.strip() == "":
        return ["the message is empty"]

    found = []

    if any(unsafe(ch, allowed="\n\t") for ch in message) or "\r" in message:
        found.append("the message has a character that is not allowed (a control, format or separator character)")

    lines = message.split("\n")

    if lines[0].strip() == "":
        found.append("the message must start with the header, with no blank line before it")
    elif len(lines) > 1 and lines[1] != "":
        found.append("a blank line must separate the header from the body")

    for problem in check_header(lines[0]):
        if problem not in found:
            found.append(problem)

    return found


def check_id(value: str) -> str:
    if not isinstance(value, str) or COMMIT_ID.fullmatch(value) is None:
        raise CheckError("an id is not a full lowercase commit id")

    return value


def read_git(root, args: list[str]) -> bytes:
    """Runs git with no shell and reads at most MAX_OUTPUT bytes of it."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0", "LC_ALL": "C"}

    try:
        proc = subprocess.Popen(
            [
                "git",
                "-C",
                str(root),
                "-c",
                "log.showSignature=false",
                "-c",
                "i18n.logOutputEncoding=UTF-8",
                *args,
            ],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env=env,
        )
    except OSError:
        raise CheckError("git could not be run") from None

    timer = threading.Timer(GIT_TIMEOUT, proc.kill)
    timer.start()

    try:
        data = proc.stdout.read(MAX_OUTPUT + 1)

        if len(data) > MAX_OUTPUT:
            proc.kill()
            raise CheckError("the history is too large to check")

        if proc.wait() != 0:
            raise CheckError("git could not list the commits (is each id in the repository?)")
    finally:
        timer.cancel()
        proc.kill()
        proc.wait()
        proc.stdout.close()

    return data


def check_range(root, base: str, head: str) -> list[str]:
    """The problems with the commits that are in head and not in base."""
    base, head = check_id(base), check_id(head)

    data = read_git(
        root,
        [
            "log",
            "-z",
            f"--max-count={MAX_COMMITS + 1}",
            "--format=%H%n%P%n%B",
            "--end-of-options",
            f"{base}..{head}",
        ],
    )

    records = [r for r in data.split(b"\0") if r]

    if len(records) > MAX_COMMITS:
        return [f"the pull request has more than {MAX_COMMITS} commits"]

    found = []

    for record in records:
        parts = record.split(b"\n", 2)

        if len(parts) != 3:
            raise CheckError("git gave a commit that could not be read")

        try:
            sha = check_id(parts[0].decode("ascii"))
        except (UnicodeDecodeError, CheckError):
            raise CheckError("git gave a commit that could not be read") from None

        who = sha[:12]

        if len(parts[1].split()) > 1:
            found.append(f"{who}: a merge commit; rebase onto main instead of merging")

        try:
            message = parts[2].decode("utf-8")
        except UnicodeDecodeError:
            found.append(f"{who}: the message is not valid UTF-8")
            continue

        found.extend(f"{who}: {problem}" for problem in check_message(message))

    return found


USAGE = "usage: check_commits.py --base ID --head ID [--root DIR]  (the title is read from PR_TITLE)"


def parse(argv: list[str]) -> dict[str, str]:
    options: dict[str, str] = {}

    i = 0

    while i < len(argv):
        name = argv[i]

        if name not in ("--base", "--head", "--root") or i + 1 >= len(argv) or name in options:
            raise CheckError(USAGE)

        options[name] = argv[i + 1]
        i += 2

    if "--base" not in options or "--head" not in options:
        raise CheckError(USAGE)

    return options


def main(argv: list[str], env) -> int:
    try:
        options = parse(argv)

        found = check_range(options.get("--root", "."), options["--base"], options["--head"])

        if "PR_TITLE" in env:
            found.extend(f"title: {problem}" for problem in check_title(env["PR_TITLE"]))
    except CheckError as err:
        print(f"check_commits: {err}", file=sys.stderr)

        return 2
    except Exception:  # noqa: BLE001  fail closed, and say nothing that could repeat the input
        print("check_commits: the check could not be made", file=sys.stderr)

        return 2

    for problem in found:
        print(problem)

    if found:
        print(f"{len(found)} problem(s). Messages are type(scope)!: subject, see CLAUDE.md (Pull requests).")

        return 1

    print("every commit and the title follow the conventional form")

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:], os.environ))
