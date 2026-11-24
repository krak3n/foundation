#!/usr/bin/env python3
"""Checks GitHub Actions workflows for the mistakes the security audit found.

    python3 .github/scripts/check_workflows.py [workflow.yml ...]

With no arguments it checks every workflow in .github/workflows. It exits 1 and prints
`file:line: message` for each problem. The rules:

  * every `uses:` is pinned to a full 40 character commit SHA, so an action can't change
    under us (a tag or branch can be moved);
  * event data (`github.event.*`, branch and tag names, workflow inputs) is not pasted into
    a `run:` script with `${{ }}`, where it becomes shell syntax: pass it through `env:`;
  * the workflow has top-level `permissions:`;
  * every checkout sets `persist-credentials: false`, so the token isn't left in `.git/config`
    for the steps that follow;
  * every job has `timeout-minutes`;
  * the private key of the release GitHub App (`secrets.RELEASE_APP_PRIVATE_KEY`) is only the
    `private-key` input of `actions/create-github-app-token`, in a workflow that only a push,
    a release, a manual run or a schedule can start (never a pull request), in a job in the
    `Release` environment, and that step asks for a token for one repository with only
    the permissions it needs; no other secret is used, and `secrets: inherit` is refused.

It reads the files as text rather than parsing YAML, so it needs nothing installed, and the
workflows here are simple enough for that. It is a guard against regressions, not a
replacement for a full linter such as zizmor, which CI also runs.
"""

import pathlib
import re
import sys

SHA = re.compile(r"^[0-9a-f]{40}$")
USES = re.compile(r"^(\s*)(?:-\s+)?uses:\s*([^\s#]+)")
RUN = re.compile(r"^(\s*)(-\s+)?run:\s*(.*)$")
EXPRESSION = re.compile(r"\$\{\{(.*?)\}\}", re.S)

# Where an expression is attacker influenced, in a script: the event payload, names of
# branches and tags, and inputs.
KEY = "RELEASE_APP_PRIVATE_KEY"
APP_ACTION = "actions/create-github-app-token@"

# The triggers a pull request (or anyone who can comment, open an issue or fork) can't start.
SAFE_TRIGGERS = {"push", "release", "workflow_dispatch", "schedule"}

# The only permissions the release token may ask for, and the levels it may ask them at.
APP_PERMISSIONS = {"permission-contents", "permission-pull-requests"}
APP_LEVELS = {"read", "write"}

UNTRUSTED = re.compile(r"github\.event\b|github\.head_ref\b|github\.ref(_name)?\b|\binputs\.")


def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def code(line: str) -> str:
    """A line without its comment (a whole line comment is empty)."""
    if line.lstrip().startswith("#"):
        return ""

    return line.split(" #")[0].rstrip()


def triggers_of(lines: list[str]) -> set[str]:
    """The events a workflow runs on, from its `on:`, in the block, inline and list forms."""
    found: set[str] = set()

    for i, line in enumerate(lines):
        m = re.match(r"""^["']?on["']?:\s*(.*)$""", code(line))

        if not m:
            continue

        inline = m.group(1).strip()

        if inline:
            found.update(w for w in re.findall(r"[A-Za-z_]+", inline))

            return found

        for nxt in lines[i + 1:]:
            text = code(nxt)

            if not text.strip():
                continue

            if indent_of(text) == 0:
                break

            key = re.match(r"^  (?:- )?([A-Za-z_]+)", text)

            if key and indent_of(text) == 2:
                found.add(key.group(1))

        return found

    return found


def step_of(lines: list[str], i: int) -> tuple[int, int]:
    """The lines of the step that line i is in."""
    start = i

    if not re.match(r"\s*- ", lines[i]):
        while start >= 0 and not (re.match(r"\s*- ", lines[start]) and indent_of(lines[start]) < indent_of(lines[i])):
            start -= 1

    if start < 0:
        return i, i + 1

    end = start + 1

    while end < len(lines) and (not lines[end].strip() or indent_of(lines[end]) > indent_of(lines[start])):
        end += 1

    return start, end


def jobs_of(lines: list[str]) -> list[tuple[int, str, int]]:
    """(line of the job's name, its name, line after its end) for each job."""
    try:
        start = next(i for i, line in enumerate(lines) if re.match(r"jobs:\s*$", line))
    except StopIteration:
        return []

    ids = [(i, lines[i].strip().rstrip(":")) for i in range(start + 1, len(lines))
           if re.match(r"  [A-Za-z0-9_-]+:\s*$", lines[i])]

    return [(i, job, ids[n + 1][0] if n + 1 < len(ids) else len(lines)) for n, (i, job) in enumerate(ids)]


def check_app(lines: list[str], problem) -> None:
    """The rules for the release App's key, its token, and secrets in general."""
    key_lines: list[int] = []

    for i, line in enumerate(lines):
        text = code(line)

        if re.match(r"\s*secrets:\s*inherit\b", text):
            problem(i + 1, "`secrets: inherit` hands every secret to another workflow; name the ones it needs")

        for expr in EXPRESSION.findall(text):
            if not re.search(r"\bsecrets\b", expr):
                continue

            name = expr.strip()

            if name == f"secrets.{KEY}":
                key_lines.append(i)
            elif name != "secrets.GITHUB_TOKEN":
                problem(i + 1, f"`{name}` is a secret that is not allowed: only `secrets.GITHUB_TOKEN` and "
                               f"`secrets.{KEY}` are used (no personal access token)")

    if key_lines:
        triggers = triggers_of(lines)
        bad = sorted(triggers - SAFE_TRIGGERS)

        if not triggers:
            problem(key_lines[0] + 1, f"`secrets.{KEY}` is used but no trigger could be read from `on:`")
        elif bad:
            problem(key_lines[0] + 1, f"`secrets.{KEY}` is used in a workflow with the trigger `{', '.join(bad)}`, "
                                      "which a pull request or an outsider can start; only push, release, "
                                      "workflow_dispatch and schedule may")

    for i in key_lines:
        start, end = step_of(lines, i)
        step = "\n".join(lines[start:end])

        if APP_ACTION not in step or not re.fullmatch(rf"\s*private-key:\s*\$\{{\{{\s*secrets\.{KEY}\s*\}}\}}", code(lines[i])):
            problem(i + 1, f"`secrets.{KEY}` may only be the `private-key` input of `{APP_ACTION.rstrip('@')}`")

    uses_key: set[int] = set()

    for i, line in enumerate(lines):
        m = USES.match(line)

        if not m or APP_ACTION not in m.group(2) or line.lstrip().startswith("#"):
            continue

        start, end = step_of(lines, i)
        inputs: dict[str, str] = {}

        for k in range(start, end):
            kv = re.match(r"^\s*(?:- )?([A-Za-z][A-Za-z0-9-]*):\s*(.*?)\s*$", code(lines[k]))

            if kv and kv.group(1) in inputs:
                problem(i + 1, f"`{kv.group(1)}` is given twice in the token step; which one wins is not for us to guess")
            elif kv:
                inputs[kv.group(1)] = kv.group(2)

        def expect(input_name: str, pattern: str, why: str) -> None:
            if re.fullmatch(pattern, inputs.get(input_name, "")) is None:
                problem(i + 1, f"`{input_name}` of the token step {why}")

        expect("client-id", r"\$\{\{\s*vars\.RELEASE_CLIENT_ID\s*\}\}", "must be `${{ vars.RELEASE_CLIENT_ID }}`")

        if "app-id" in inputs:
            problem(i + 1, "`app-id` is deprecated in favour of `client-id`, and a leftover would hide a missing one; remove it")
        expect("private-key", rf"\$\{{\{{\s*secrets\.{KEY}\s*\}}\}}", f"must be `${{{{ secrets.{KEY} }}}}`")
        expect("owner", r"\S.*", "must be set")
        expect("repositories", r"[A-Za-z0-9._-]+", "must be one repository, written out")

        if "skip-token-revoke" in inputs:
            problem(i + 1, "`skip-token-revoke` leaves the token valid after the job; remove it")

        asked = {k for k in inputs if k.startswith("permission-")}

        for extra in sorted(asked - APP_PERMISSIONS):
            problem(i + 1, f"`{extra}`: the token may only ask for {', '.join(sorted(APP_PERMISSIONS))}")

        for need in sorted(APP_PERMISSIONS - asked):
            problem(i + 1, f"the token must ask for `{need}`, so it has only the permission it needs (permission)")

        for name in sorted(asked & APP_PERMISSIONS):
            if inputs[name] not in APP_LEVELS:
                problem(i + 1, f"`{name}` must be read or write (permission)")

        uses_key.add(i)

    for first, job, end in jobs_of(lines):
        if any(first < k < end for k in key_lines) or any(first < k < end for k in uses_key):
            if not any(re.fullmatch(r"    environment: Release\s*", lines[k]) for k in range(first + 1, end)):
                problem(first + 1, f"job '{job}' makes the App's token, so it must say `environment: Release` "
                                   "(the key is a secret of that environment)")


def check_text(text: str, name: str) -> list[str]:
    lines = text.splitlines()
    found: list[str] = []

    def problem(number: int, message: str) -> None:
        found.append(f"{name}:{number}: {message}")

    # permissions
    if not any(re.match(r"permissions:", line) for line in lines):
        problem(1, "no top-level `permissions:`; set the least the jobs need, or `permissions: {}`")

    for i, line in enumerate(lines, 1):
        # pinning
        m = USES.match(line)
        if m and not line.lstrip().startswith("#"):
            target = m.group(2)

            if not target.startswith(("./", "docker://")):
                ref = target.partition("@")[2]

                if not SHA.match(ref):
                    problem(i, f"`{target.split('@')[0]}` is not pinned to a commit SHA (found {ref or 'no ref'!r}); "
                               "pin the full 40 character SHA and keep the version in a comment")

        # injection
        m = RUN.match(line)
        if m and not line.lstrip().startswith("#"):
            key_indent = len(m.group(1)) + (2 if m.group(2) else 0)
            rest = m.group(3).split(" #")[0].strip()

            body: list[tuple[int, str]] = []

            if rest and rest[0] not in "|>":
                body.append((i, rest))
            else:
                j = i
                while j < len(lines):
                    nxt = lines[j]

                    if nxt.strip() and indent_of(nxt) <= key_indent:
                        break

                    body.append((j + 1, nxt))
                    j += 1

            for number, content in body:
                for expr in EXPRESSION.findall(content):
                    if UNTRUSTED.search(expr):
                        problem(number, f"`${{{{ {expr.strip()} }}}}` in a script is shell injection waiting to happen; "
                                        "pass it through `env:` and use the variable in the script")

    # checkout must not keep credentials
    for i, line in enumerate(lines, 1):
        m = USES.match(line)
        if not m or "actions/checkout@" not in m.group(2):
            continue

        step_indent = indent_of(line) - (2 if line.lstrip().startswith("-") else 0)
        step = [line]

        for nxt in lines[i:]:
            if nxt.strip() and indent_of(nxt) <= step_indent and nxt.lstrip().startswith("- "):
                break

            if nxt.strip() and indent_of(nxt) < step_indent:
                break

            step.append(nxt)

        if not any(re.match(r"\s*persist-credentials:\s*false\b", s) for s in step):
            problem(i, "`actions/checkout` should set `persist-credentials: false` so the token isn't left in .git/config")

    check_app(lines, problem)

    # every job needs a timeout
    for i, job, end in jobs_of(lines):
        if not any(re.match(r"    timeout-minutes:", lines[k]) for k in range(i + 1, end)):
            problem(i + 1, f"job '{job}' has no `timeout-minutes`")

    return found


def main(argv: list[str]) -> int:
    paths = [pathlib.Path(a) for a in argv] or sorted(pathlib.Path(".github/workflows").glob("*.y*ml"))

    if not paths:
        print("no workflows found", file=sys.stderr)

        return 1

    problems: list[str] = []

    for path in paths:
        problems += check_text(path.read_text(), str(path))

    for p in problems:
        print(p)

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
