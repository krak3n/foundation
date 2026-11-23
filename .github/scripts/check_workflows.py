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
  * every job has `timeout-minutes`.

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
UNTRUSTED = re.compile(r"github\.event\b|github\.head_ref\b|github\.ref(_name)?\b|\binputs\.")


def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


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

    # every job needs a timeout
    try:
        start = next(i for i, line in enumerate(lines) if re.match(r"jobs:\s*$", line))
    except StopIteration:
        return found

    job_ids = [(i, lines[i].strip().rstrip(":")) for i in range(start + 1, len(lines))
               if re.match(r"  [A-Za-z0-9_-]+:\s*$", lines[i])]

    for n, (i, job) in enumerate(job_ids):
        end = job_ids[n + 1][0] if n + 1 < len(job_ids) else len(lines)

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
