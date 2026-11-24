"""Tests for check_workflows.py. Run: python3 -m unittest discover -s .github/scripts

The checker is a regression guard for the two mistakes the security audit found in the
release pipeline: actions that can change under us, and event data pasted into shell
scripts. The last test runs it on the real workflows.
"""

import pathlib
import re
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


APP = f"""\
name: App

on:
  push:
    branches: [main]
  workflow_dispatch:

permissions: {{}}

jobs:
  release:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    environment: Release
    permissions:
      contents: write
      pull-requests: write
    steps:
      - id: app
        uses: actions/create-github-app-token@{SHA} # v2.0.0
        with:
          app-id: ${{{{ vars.RELEASE_APP_ID }}}}
          private-key: ${{{{ secrets.RELEASE_APP_PRIVATE_KEY }}}}
          owner: ${{{{ github.repository_owner }}}}
          repositories: foundation
          permission-contents: write
          permission-pull-requests: write

      - uses: googleapis/release-please-action@{SHA} # v5.0.0
        with:
          token: ${{{{ steps.app.outputs.token }}}}
"""


# The App's private key can write to the repository, so it is only ever handed to the
# step that mints a short lived token, in a workflow that no pull request can start, in a
# job that names the Release environment, and the token is as narrow as it can be.
class AppToken(unittest.TestCase):
    def test_a_workflow_that_mints_a_token_correctly_has_no_problems(self):
        self.assertEqual(problems(APP), [])

    # --- where the key may be used -----------------------------------------------

    def test_the_key_is_refused_in_a_workflow_a_pull_request_can_start(self):
        for trigger in [
            "pull_request:",
            "pull_request_target:",
            "pull_request_review:",
            "workflow_run:",
            "issue_comment:",
            "issues:",
            "fork:",
        ]:
            text = APP.replace("  workflow_dispatch:\n", f"  {trigger}\n")
            self.assertTrue(has(problems(text), "RELEASE_APP_PRIVATE_KEY", "trigger"), trigger)

    def test_the_other_ways_of_writing_a_trigger_are_refused_too(self):
        on_block = "on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n"

        for replacement in [
            "on: pull_request\n",
            "on: [push, pull_request]\n",
            "on: [push, pull_request_target]\n",
            'on: ["pull_request"]\n',
            "on:\n  push:\n    branches: [main]\n  pull_request:\n    branches: [main]\n",
            "on:\n  pull_request_target: {}\n",
        ]:
            text = APP.replace(on_block, replacement)
            self.assertNotEqual(text, APP, repr(replacement))
            self.assertTrue(has(problems(text), "RELEASE_APP_PRIVATE_KEY", "trigger"), repr(replacement))

    def test_a_workflow_with_no_trigger_we_can_read_is_refused(self):
        on_block = "on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n"

        self.assertTrue(has(problems(APP.replace(on_block, "")), "RELEASE_APP_PRIVATE_KEY", "trigger"))

    def test_push_release_dispatch_and_schedule_are_allowed(self):
        on_block = "on:\n  push:\n    branches: [main]\n  workflow_dispatch:\n"

        for replacement in [
            "on:\n  release:\n    types: [published]\n",
            "on:\n  schedule:\n    - cron: '0 3 * * 1'\n",
            "on: push\n",
            "on: [push, workflow_dispatch]\n",
        ]:
            self.assertEqual(problems(APP.replace(on_block, replacement)), [], repr(replacement))

    def test_the_key_is_only_ever_the_private_key_input_of_the_token_step(self):
        for line, where in [
            ("      - run: echo hi\n        env:\n          KEY: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}\n", "env"),
            ("      - run: echo '${{ secrets.RELEASE_APP_PRIVATE_KEY }}'\n", "run"),
            (
                "      - uses: googleapis/release-please-action@" + SHA + "\n"
                "        with:\n          token: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}\n",
                "with",
            ),
        ]:
            self.assertTrue(has(problems(APP + line), "RELEASE_APP_PRIVATE_KEY"), where)

    def test_a_comment_that_names_the_key_is_fine_anywhere(self):
        text = APP + "      # the key is RELEASE_APP_PRIVATE_KEY, secrets.RELEASE_APP_PRIVATE_KEY\n"
        self.assertEqual(problems(text), [])

        pull = APP.replace("  workflow_dispatch:\n", "  pull_request:\n").replace(
            "          private-key: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}\n", ""
        )
        self.assertFalse(has(problems(pull), "trigger"), problems(pull))

    # --- no other secret, no personal token ----------------------------------------

    def test_a_secret_other_than_the_workflow_token_and_the_key_is_refused(self):
        for name in ["RELEASE_PAT", "GH_TOKEN", "PERSONAL_ACCESS_TOKEN", "release_app_private_key", "RELEASE_APP_PRIVATE_KEY_OLD"]:
            text = APP + f"      - run: echo hi\n        env:\n          T: ${{{{ secrets.{name} }}}}\n"
            self.assertTrue(has(problems(text), "secrets." + name), name)

    def test_the_workflow_token_and_an_index_form_are_handled(self):
        ok = APP + "      - run: echo hi\n        env:\n          T: ${{ secrets.GITHUB_TOKEN }}\n"
        self.assertEqual(problems(ok), [])

        for form in ["secrets['RELEASE_PAT']", 'secrets["RELEASE_PAT"]', "toJSON(secrets)", "secrets"]:
            text = APP + f"      - run: echo hi\n        env:\n          T: ${{{{ {form} }}}}\n"
            self.assertTrue(has(problems(text), "secrets"), form)

    def test_inheriting_every_secret_is_refused(self):
        text = APP + "  other:\n    uses: ./.github/workflows/x.yml\n    secrets: inherit\n    timeout-minutes: 1\n"
        self.assertTrue(has(problems(text), "secrets: inherit"))

    # --- the token step ---------------------------------------------------------------

    def test_the_token_action_must_be_pinned(self):
        for ref in ["v2", "v2.0.0", "main"]:
            text = APP.replace(f"create-github-app-token@{SHA}", f"create-github-app-token@{ref}")
            self.assertTrue(has(problems(text), "create-github-app-token", "pinned"), ref)

    def test_the_token_is_for_one_repository(self):
        for replacement in [
            "",
            "          repositories: foundation, other\n",
            "          repositories: foundation,other\n",
            "          repositories: |\n            foundation\n            other\n",
            "          repositories:\n",
            "          repositories: ''\n",
        ]:
            text = APP.replace("          repositories: foundation\n", replacement)
            self.assertTrue(has(problems(text), "repositories"), repr(replacement))

    def test_the_token_names_its_owner(self):
        text = APP.replace("          owner: ${{ github.repository_owner }}\n", "")
        self.assertTrue(has(problems(text), "owner"))

    def test_the_token_names_what_it_may_do_and_only_that(self):
        for missing in ["          permission-contents: write\n", "          permission-pull-requests: write\n"]:
            self.assertTrue(has(problems(APP.replace(missing, "")), "permission"), missing)

        everything = APP.replace("          permission-contents: write\n", "").replace(
            "          permission-pull-requests: write\n", ""
        )
        self.assertTrue(has(problems(everything), "permission"))

        for extra in [
            "          permission-workflows: write\n",
            "          permission-administration: write\n",
            "          permission-secrets: read\n",
            "          permission-actions: write\n",
            "          permission-contents: admin\n",
        ]:
            text = APP.replace("          permission-contents: write\n", "          permission-contents: write\n" + extra)
            self.assertTrue(has(problems(text), "permission"), extra)

    def test_the_token_is_not_kept_after_the_job(self):
        text = APP.replace("          repositories: foundation\n", "          repositories: foundation\n          skip-token-revoke: true\n")
        self.assertTrue(has(problems(text), "skip-token-revoke"))

    def test_the_app_id_is_a_variable_and_the_key_comes_from_secrets(self):
        for line in [
            "          app-id: 123456\n",
            "          app-id: ${{ secrets.RELEASE_APP_ID }}\n",
            "          app-id: ${{ vars.SOMETHING_ELSE }}\n",
        ]:
            text = APP.replace("          app-id: ${{ vars.RELEASE_APP_ID }}\n", line)
            self.assertTrue(has(problems(text), "app-id"), line)

        for line in ["          private-key: |\n            -----BEGIN RSA PRIVATE KEY-----\n", "          private-key: abc\n"]:
            text = APP.replace("          private-key: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}\n", line)
            self.assertTrue(has(problems(text), "private-key"), line)

    # --- the job -----------------------------------------------------------------------

    def test_the_job_must_name_the_release_environment(self):
        for replacement in ["", "    environment: release\n", "    environment: production\n", "    environment: ${{ inputs.env }}\n"]:
            text = APP.replace("    environment: Release\n", replacement)
            self.assertTrue(has(problems(text), "environment", "Release"), repr(replacement))

    def test_each_job_is_checked_on_its_own(self):
        second = APP.split("jobs:\n")[1].replace("  release:", "  other:").replace("    environment: Release\n", "")
        found = problems(APP + second)

        self.assertTrue(has(found, "other", "environment"), found)
        self.assertFalse(has(found, "'release'", "environment"), found)

    def test_a_job_that_does_not_use_the_key_needs_no_environment(self):
        self.assertEqual(problems(GOOD), [])


class AppTokenInThisRepository(unittest.TestCase):
    ROOT = pathlib.Path(__file__).resolve().parents[2]

    def workflow(self, name: str) -> str:
        return (self.ROOT / ".github" / "workflows" / name).read_text()

    def test_the_two_release_workflows_use_a_token_from_the_app(self):
        for name in ["release-please.yml", "release-cascade.yml"]:
            text = self.workflow(name)

            self.assertIn("actions/create-github-app-token@", text, name)
            self.assertIn("environment: Release", text, name)
            self.assertRegex(text, r"(?m)^\s+token: \$\{\{ steps\.[A-Za-z0-9_-]+\.outputs\.token \}\}\s*$", name)
            self.assertNotIn("secrets.GITHUB_TOKEN", text, name + " should use the App's token")

    def test_the_cascade_names_the_branch_it_opens_its_pull_request_against(self):
        # A release starts the cascade on its tag, which is a detached commit, and
        # create-pull-request refuses to guess a base then. A dispatch from main is a
        # branch, so rehearsing by hand does not show it: the first real release did.
        text = self.workflow("release-cascade.yml")

        self.assertRegex(text, r"(?m)^\s+types: \[published\]", "the cascade should run on a release")

        start = text.index("peter-evans/create-pull-request@")
        step = text[start:]

        self.assertRegex(step, r"(?m)^\s+base: main\s*$", "create-pull-request needs `base: main` on a tag checkout")

    def test_no_other_workflow_can_reach_the_key(self):
        for path in sorted((self.ROOT / ".github" / "workflows").glob("*.y*ml")):
            if path.name in ("release-please.yml", "release-cascade.yml"):
                continue

            text = path.read_text()

            self.assertNotIn("RELEASE_APP_PRIVATE_KEY", text, path.name)
            self.assertNotIn("create-github-app-token", text, path.name)

    def test_the_pull_requests_the_cascade_opens_pass_the_commit_check(self):
        import check_commits

        text = self.workflow("release-cascade.yml")

        for key in ["title", "commit-message"]:
            found = re.search(rf'(?m)^\s+{key}: "(.*)"\s*$', text)

            self.assertIsNotNone(found, key)

            for tag in ["v0.1.0", "v12.34.56", "v1.2.3-rc.1", "v0.0.1-rehearsal.10"]:
                header = found.group(1).replace("${{ env.TAG }}", tag)

                self.assertEqual(check_commits.check_message(header), [], f"{key} for {tag}")
                self.assertEqual(check_commits.check_title(header), [], f"{key} for {tag}")

    def test_the_documents_say_how_the_key_is_kept(self):
        for name in ["CLAUDE.md", "SECURITY.md"]:
            text = (self.ROOT / name).read_text()

            self.assertIn("RELEASE_APP_PRIVATE_KEY", text, name)
            self.assertIn("Release", text, name)


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
