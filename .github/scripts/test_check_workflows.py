"""Tests for check_workflows.py. Run: python3 -m unittest discover -s .github/scripts

The checker is a regression guard for the two mistakes the security audit found in the
release pipeline: actions that can change under us, and event data pasted into shell
scripts. The last test runs it on the real workflows.
"""

import pathlib
import unittest

import check_workflows as cw

SHA = "11d5960a326750d5838078e36cf38b85af677262"

GOOD = f"""\
name: Good

on:
  push:
    branches: [main]

permissions: {{}}

jobs:
  test:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@{SHA} # v4.4.0
        with:
          persist-credentials: false

      - name: Use the tag
        env:
          TAG: ${{{{ github.event.release.tag_name }}}}
        run: |
          echo "$TAG"
          go test ./...

      - uses: ./.github/actions/local
"""


def problems(text: str) -> list[str]:
    return cw.check_text(text, "wf.yml")


def has(found: list[str], *words: str) -> bool:
    return any(all(w in p for w in words) for p in found)


class CheckWorkflows(unittest.TestCase):
    def test_a_good_workflow_has_no_problems(self):
        self.assertEqual(problems(GOOD), [])

    # --- pinning -------------------------------------------------------------

    def test_actions_must_be_pinned_to_a_full_commit_sha(self):
        for ref in ["v4", "v4.4.0", "main", "master", "latest", "11d5960", SHA[:39], SHA.upper() + "0"]:
            text = GOOD.replace(f"actions/checkout@{SHA}", f"actions/checkout@{ref}")
            self.assertTrue(has(problems(text), "checkout", "pinned"), ref)

    def test_a_third_party_action_on_a_branch_is_reported_with_its_line(self):
        text = GOOD + "      - uses: mikefarah/yq@master\n"
        found = problems(text)

        self.assertTrue(has(found, "mikefarah/yq", "pinned"), found)
        self.assertTrue(any(p.startswith(f"wf.yml:{text.count(chr(10))}:") for p in found), found)

    def test_an_action_with_no_ref_is_reported(self):
        self.assertTrue(has(problems(GOOD + "      - uses: actions/setup-go\n"), "setup-go", "pinned"))

    def test_a_subdirectory_action_is_checked_too(self):
        text = GOOD + "      - uses: github/codeql-action/init@v3\n"
        self.assertTrue(has(problems(text), "codeql-action/init", "pinned"))

    def test_local_actions_and_comments_are_fine(self):
        text = GOOD + "      # uses: actions/checkout@v4 (in a comment)\n"
        self.assertEqual(problems(text), [])

    # --- injection -----------------------------------------------------------

    def test_event_data_in_a_script_is_reported(self):
        for expr in [
            "github.event.release.tag_name",
            "github.event.pull_request.title",
            "github.head_ref",
            "github.ref_name",
            "github.ref",
            "inputs.tag",
            "github.event.inputs.tag",
        ]:
            text = GOOD.replace('go test ./...', f'VERSION="${{{{ {expr} }}}}"')
            self.assertTrue(has(problems(text), "script", expr.split(".")[0]), expr)

    def test_a_single_line_run_is_checked(self):
        text = GOOD + '      - run: echo "${{ github.event.release.tag_name }}"\n'
        self.assertTrue(has(problems(text), "tag_name"))

    def test_the_line_of_the_expression_is_reported(self):
        text = GOOD.replace("go test ./...", 'echo "${{ github.head_ref }}"')
        line = text.splitlines().index('          echo "${{ github.head_ref }}"') + 1

        self.assertTrue(any(p.startswith(f"wf.yml:{line}:") for p in problems(text)), problems(text))

    def test_the_same_expressions_are_fine_in_env_with_and_if(self):
        text = GOOD + (
            "      - uses: actions/setup-go@" + SHA + "\n"
            "        with:\n"
            "          go-version: ${{ matrix.go-version }}\n"
            "          title: ${{ github.event.release.tag_name }}\n"
            "        if: startsWith(github.event.release.tag_name, 'v')\n"
        )
        self.assertEqual(problems(text), [])

    def test_harmless_expressions_in_a_script_are_fine(self):
        text = GOOD.replace("go test ./...", 'echo "${{ runner.os }} ${{ matrix.go-version }} ${{ github.sha }}"')
        self.assertEqual(problems(text), [])

    # --- permissions, credentials, timeouts ------------------------------------

    def test_a_workflow_needs_top_level_permissions(self):
        self.assertTrue(has(problems(GOOD.replace("permissions: {}\n", "")), "permissions"))

    def test_a_checkout_must_not_keep_the_token(self):
        kept = "        with:\n          persist-credentials: false\n"

        for replacement in ["", "        with:\n          persist-credentials: true\n"]:
            text = GOOD.replace(kept, replacement)
            self.assertTrue(has(problems(text), "checkout", "persist-credentials"), repr(replacement))

    def test_every_job_needs_a_timeout(self):
        text = GOOD.replace("    timeout-minutes: 15\n", "")
        self.assertTrue(has(problems(text), "test", "timeout"))

    def test_each_job_is_checked_on_its_own(self):
        second = GOOD.split("jobs:\n")[1].replace("  test:", "  other:").replace("    timeout-minutes: 15\n", "")
        found = problems(GOOD + second)

        self.assertTrue(has(found, "other", "timeout"), found)
        self.assertFalse(has(found, "'test'", "timeout"), found)


class RepositoryWorkflows(unittest.TestCase):
    def test_the_workflows_in_this_repository_pass(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        files = sorted((root / ".github" / "workflows").glob("*.y*ml"))

        self.assertTrue(files, "no workflows found")

        found = []
        for f in files:
            found += cw.check_text(f.read_text(), str(f.relative_to(root)))

        self.assertEqual(found, [], "\n" + "\n".join(found))


if __name__ == "__main__":
    unittest.main()
