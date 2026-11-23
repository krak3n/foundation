"""Tests for check_commits.py. Run: python3 -m unittest discover -s .github/scripts

The checker holds every commit of a pull request (and its title, in case it is squashed)
to the conventional commit form, because release-please writes the changelog and picks
the version from the messages: `type(scope)!: subject`, a lowercase subject, a header of
at most 72 characters. A message comes from whoever opens the pull request, so it is
only ever read as text, never put in a command, and what the checker says about it is a
fixed reason with the short id of the commit and never any of its text. Anything that
can't be understood fails.

The first tests are for the rules, then hostile input, then a real git repository, the
command line, and last how it is wired into CI, the Makefile and the docs.
"""

import contextlib
import io
import json
import os
import pathlib
import random
import re
import subprocess
import tempfile
import unittest

import check_commits as cc

REPO = pathlib.Path(__file__).resolve().parents[2]

CANARY = "canary-secret-9d2e"


def has(found: list[str], *words: str) -> bool:
    return any(all(w in p for w in words) for p in found)


class Headers(unittest.TestCase):
    def test_these_are_conventional(self):
        for header in [
            "feat: add a thing",
            "fix: stop a leak",
            "docs: explain the quick start",
            "test: cover the hostile cases",
            "refactor: split the loader",
            "perf: avoid a copy",
            "ci: pin the actions",
            "build: raise the go line",
            "chore: init",
            "revert: undo the cooldown",
            "style: format the examples",
            "feat(config): add the env provider",
            "fix(http): refuse a bad address",
            "feat(internal): add safetext",
            "feat(watch): poll with stat only",
            "feat(interrupt): handle signals",
            "docs(examples): add example programs",
            "feat(yaml): add the provider",
            "chore(deps): bump go.yaml.in/yaml/v3 from 3.0.3 to 3.0.4",
            "chore(deps): Bump actions/checkout from 4.4.0 to 7.0.1",
            "chore(deps)!: Bump the only thing that is allowed a capital",
            "feat!: remove the old option",
            "feat(http)!: remove the old option",
            "fix: handle `nil` providers",
            "feat: 3 providers are read in order",
            "test: cover 日本語 names",
            "fix: don't drop the second key",
            "feat: " + "x" * (72 - len("feat: ")),
        ]:
            with self.subTest(header):
                self.assertEqual(cc.check_header(header), [])

    def test_these_are_not(self):
        for header, words in {
            "add a thing": ("header",),
            "Add a thing": ("header",),
            "Feat: add a thing": ("type",),
            "FEAT: add a thing": ("type",),
            "feature: add a thing": ("type",),
            "wip: add a thing": ("type",),
            "feat add a thing": ("header",),
            "feat:add a thing": ("header",),
            "feat:  add a thing": ("header",),
            "feat : add a thing": ("header",),
            "feat(config) : add a thing": ("header",),
            "feat (config): add a thing": ("header",),
            "feat!(config): add a thing": ("header",),
            "feat(config)!!: add a thing": ("header",),
            "feat(): add a thing": ("scope",),
            "feat(unknown): add a thing": ("scope",),
            "feat(Config): add a thing": ("scope",),
            "feat(config,http): add a thing": ("scope",),
            "feat(config/http): add a thing": ("scope",),
            "feat((config)): add a thing": ("scope",),
            "feat(config: add a thing": ("header",),
            "feat: Add a thing": ("lowercase",),
            "chore: Bump actions/checkout": ("lowercase",),
            "chore(config): Bump actions/checkout": ("lowercase",),
            "fix(deps): Bump actions/checkout": ("lowercase",),
            "feat(deps): Bump actions/checkout": ("lowercase",),
            "feat: Éclair": ("lowercase",),
            "feat: add a thing.": ("full stop",),
            "feat: add a thing...": ("full stop",),
            "feat: ": ("subject",),
            "feat:": ("header",),
            "feat(config): ": ("subject",),
            "": ("header",),
            " ": ("header",),
            " feat: add a thing": ("header",),
            "feat: add a thing ": ("header",),
            "\tfeat: add a thing": ("header",),
            "fixup! feat: add a thing": ("type",),
            "squash! feat: add a thing": ("type",),
            "Merge branch 'main' into bootstrap": ("header",),
            'Revert "feat: add a thing"': ("header",),
            "WIP: add a thing": ("type",),
            "feat: " + "x" * (73 - len("feat: ")): ("72",),
            "feat: " + "x" * 100_000: ("72",),
        }.items():
            with self.subTest(header[:60]):
                found = cc.check_header(header)

                self.assertTrue(found, "was accepted")
                self.assertTrue(has(found, *words), f"{found} does not say {words}")

    def test_characters_that_hide_or_forge_text_are_refused(self):
        for name, char in {
            "a tab": "\t",
            "a newline": "\n",
            "a carriage return": "\r",
            "NUL": "\x00",
            "escape": "\x1b",
            "delete": "\x7f",
            "a C1 control": "\x85",
            "a right to left override": "\u202e",
            "a bidi isolate": "\u2066",
            "a zero width space": "\u200b",
            "a zero width joiner": "\u200d",
            "a byte order mark": "\ufeff",
            "a line separator": " ",
            "a paragraph separator": " ",
            "a no-break space": "\u00a0",
            "an ideographic space": "\u3000",
            "a lone surrogate": "\ud800",
            "a private use character": "\ue000",
            "a non-character": "￾",
        }.items():
            for where, header in {
                "in the subject": f"feat: add{char}thing",
                "at the end": f"feat: add thing{char}",
                "in the scope": f"feat(con{char}fig): add thing",
                "in the type": f"fe{char}at: add thing",
            }.items():
                with self.subTest(f"{name} {where}"):
                    self.assertTrue(cc.check_header(header), "was accepted")

    def test_the_rules_hold_for_every_combination(self):
        types = [("feat", True), ("fix", True), ("chore", True), ("feature", False), ("Feat", False), ("", False)]
        scopes = [("", True), ("(config)", True), ("(deps)", True), ("(nope)", False), ("()", False)]
        bangs = [("", True), ("!", True), ("!!", False)]
        subjects = [("add it", True), ("Add it", False), ("add it.", False), ("", False), ("add  it", True)]

        for (t, t_ok) in types:
            for (s, s_ok) in scopes:
                for (b, b_ok) in bangs:
                    for (j, j_ok) in subjects:
                        header = f"{t}{s}{b}: {j}"

                        # Dependabot writes "chore(deps): Bump ...", so a capital is allowed there.
                        capital_ok = j == "Add it" and t == "chore" and s == "(deps)"

                        with self.subTest(header):
                            got = cc.check_header(header) == []
                            self.assertEqual(got, t_ok and s_ok and b_ok and (j_ok or capital_ok))

    def test_the_lists_are_what_the_docs_say(self):
        self.assertEqual(
            set(cc.TYPES),
            {"feat", "fix", "docs", "test", "refactor", "perf", "ci", "build", "chore", "revert", "style"},
        )
        self.assertEqual(
            set(cc.SCOPES),
            {"internal", "config", "watch", "interrupt", "http", "yaml", "examples", "deps"},
        )
        self.assertEqual(cc.MAX_HEADER, 72)


class Messages(unittest.TestCase):
    def test_a_header_alone_is_fine(self):
        for message in ["feat: add a thing", "feat: add a thing\n"]:
            with self.subTest(repr(message)):
                self.assertEqual(cc.check_message(message), [])

    def test_a_body_and_trailers_are_fine(self):
        message = (
            "feat(config): add the env provider\n"
            "\n"
            "Long lines are fine in a body, as many of ours are. " + "word " * 80 + "\n"
            "\n"
            "A second paragraph, with a tab:\there, and 日本語.\n"
            "\n"
            "BREAKING CHANGE: the old option is gone\n"
            "Co-Authored-By: Someone <someone@example.com>\n"
        )

        self.assertEqual(cc.check_message(message), [])

    def test_the_header_is_held_to_the_rules(self):
        self.assertTrue(has(cc.check_message("Add a thing\n\nbody\n"), "header"))
        self.assertTrue(has(cc.check_message("feat: Add a thing\n\nbody\n"), "lowercase"))

    def test_the_body_is_set_off_by_a_blank_line(self):
        self.assertTrue(has(cc.check_message("feat: add a thing\nthe body\n"), "blank line"))

    def test_there_is_no_blank_line_before_the_header(self):
        self.assertTrue(cc.check_message("\nfeat: add a thing\n"))
        self.assertTrue(cc.check_message("\n\nfeat: add a thing\n"))

    def test_nothing_at_all_is_not_a_message(self):
        for message in ["", "\n", "   ", "\n\n\n"]:
            with self.subTest(repr(message)):
                self.assertTrue(cc.check_message(message))

    def test_a_message_has_a_limit(self):
        body = "x" * (cc.MAX_MESSAGE - len("feat: add a thing\n\n"))

        self.assertEqual(cc.check_message("feat: add a thing\n\n" + body), [])
        self.assertTrue(has(cc.check_message("feat: add a thing\n\n" + body + "x"), "long"))
        self.assertTrue(has(cc.check_message("feat: add a thing\n\n" + "x" * 10_000_000), "long"))

    def test_the_limit_is_in_bytes(self):
        body = "日" * (cc.MAX_MESSAGE // 3)

        self.assertTrue(has(cc.check_message("feat: add a thing\n\n" + body), "long"))

    def test_a_body_cannot_hide_text(self):
        for name, char in {
            "NUL": "\x00",
            "escape": "\x1b",
            "delete": "\x7f",
            "a carriage return": "\r",
            "a right to left override": "\u202e",
            "a zero width space": "\u200b",
            "a line separator": " ",
            "a lone surrogate": "\ud800",
        }.items():
            with self.subTest(name):
                found = cc.check_message(f"feat: add a thing\n\nthe {char} body\n")

                self.assertTrue(has(found, "character"), found)

    def test_a_title_is_a_header(self):
        self.assertEqual(cc.check_title("feat: add the foundation framework"), [])
        self.assertTrue(has(cc.check_title("Add the framework"), "header"))
        self.assertTrue(cc.check_title(""))
        self.assertTrue(cc.check_title("feat: add a thing\nfix: another"))
        self.assertTrue(has(cc.check_title("feat: " + "x" * 5000), "72"))


class Hostile(unittest.TestCase):
    """What a pull request author controls: every message and the title."""

    def test_no_reason_repeats_the_message(self):
        for message in [
            f"{CANARY}: add it",
            f"feat({CANARY}): add it",
            f"feat: Add {CANARY}",
            f"feat: add {CANARY}.",
            f"feat: add {CANARY}\x1b[31m",
            f"feat: add it\n{CANARY}",
            f"feat: add it\n\n{CANARY}\x00",
            f"{CANARY}" * 100,
            f"feat: add it\n\n{CANARY}" + "x" * 10_000,
            f"{CANARY}\n\n{CANARY}",
        ]:
            with self.subTest(message[:40]):
                for found in (cc.check_message(message), cc.check_header(message), cc.check_title(message)):
                    for problem in found:
                        self.assertNotIn("canary", problem)

    def test_reasons_are_short_and_plain(self):
        rng = random.Random(7)
        alphabet = list("featfixchore():!. \n\t\r") + ["\x00", "\x1b", "‮", "​", "\ud800", "日", "É", "é", "BREAKING CHANGE: "]

        for _ in range(4000):
            message = "".join(rng.choice(alphabet) for _ in range(rng.randint(0, 40)))

            for found in (cc.check_message(message), cc.check_header(message), cc.check_title(message)):
                self.assertIsInstance(found, list)

                for problem in found:
                    self.assertIsInstance(problem, str)
                    self.assertLessEqual(len(problem), 200)
                    self.assertTrue(problem.isprintable(), repr(problem))
                    self.assertTrue(problem.isascii(), repr(problem))

    def test_it_answers_the_same_each_time(self):
        for message in ["feat: Add it.", "x", "feat: add it\nbody", "feat: add it\n\nok\n"]:
            self.assertEqual(cc.check_message(message), cc.check_message(message))

    def test_what_is_accepted_is_well_formed(self):
        # The oracle: the form, written out again as a regular expression.
        form = re.compile(
            r"(feat|fix|docs|test|refactor|perf|ci|build|chore|revert|style)"
            r"(\((internal|config|watch|interrupt|http|yaml|examples|deps)\))?!?: "
            r"[^\sA-Z][^\n]*",
            re.ASCII | re.DOTALL,
        )
        deps = re.compile(r"chore\(deps\)!?: [^\s][^\n]*", re.ASCII | re.DOTALL)
        rng = random.Random(11)
        pieces = ["feat", "fix", "chore", "(config)", "(deps)", "(x)", "!", ": ", ":", " ", "add", "Add", "it", ".", "日本", "\t", "‮"]

        for _ in range(20000):
            header = "".join(rng.choice(pieces) for _ in range(rng.randint(1, 9)))

            if cc.check_header(header) == []:
                # Only Dependabot's chore(deps) may start its subject with a capital.
                self.assertTrue((deps if header.startswith("chore(deps)") else form).fullmatch(header), repr(header))
                self.assertLessEqual(len(header), 72)
                self.assertFalse(header.endswith("."))
                self.assertTrue(header.isprintable())


def git(root, *args, input=None, env=None):
    full = {
        **os.environ,
        # What the tests make must not depend on, or write to, the machine's own settings.
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": "A",
        "GIT_AUTHOR_EMAIL": "a@example.com",
        "GIT_COMMITTER_NAME": "A",
        "GIT_COMMITTER_EMAIL": "a@example.com",
        **(env or {}),
    }

    result = subprocess.run(
        ["git", "-C", str(root), "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", *args],
        input=input,
        capture_output=True,
        env=full,
        check=True,
    )

    return result.stdout.decode().strip()


def commit(root, message: bytes) -> str:
    git(root, "commit", "-q", "--allow-empty", "--no-verify", "-F", "-", input=message)

    return git(root, "rev-parse", "HEAD")


class Range(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)

        self.root = pathlib.Path(tmp.name)

        git(self.root, "init", "-q")

        self.base = commit(self.root, b"chore: init")

    def test_good_commits_pass(self):
        commit(self.root, b"feat(config): add a thing\n\nwhy it is needed\n\nCo-Authored-By: A <a@example.com>\n")
        commit(self.root, b"fix: stop a leak")

        head = git(self.root, "rev-parse", "HEAD")

        self.assertEqual(cc.check_range(self.root, self.base, head), [])

    def test_a_bad_commit_is_named_by_its_id_and_never_its_text(self):
        commit(self.root, b"feat: fine")
        bad = commit(self.root, f"Fixed {CANARY} stuff.\n\nbody\n".encode())
        commit(self.root, b"fix: also fine")

        found = cc.check_range(self.root, self.base, git(self.root, "rev-parse", "HEAD"))

        self.assertEqual(len(found), 1, found)
        self.assertTrue(found[0].startswith(bad[:12] + ": "), found)
        self.assertNotIn("canary", found[0])

    def test_every_bad_commit_is_reported(self):
        first = commit(self.root, b"bad one")
        second = commit(self.root, b"Bad: two")

        found = cc.check_range(self.root, self.base, git(self.root, "rev-parse", "HEAD"))

        self.assertTrue(has(found, first[:12]) and has(found, second[:12]), found)

    def test_only_the_commits_of_the_range_are_held_to_it(self):
        old = commit(self.root, b"an old commit made before the rule")
        base = commit(self.root, b"feat: fine")
        commit(self.root, b"fix: fine")

        found = cc.check_range(self.root, base, git(self.root, "rev-parse", "HEAD"))

        self.assertEqual(found, [])
        self.assertTrue(has(cc.check_range(self.root, self.base, base), old[:12]))

    def test_an_empty_range_is_fine(self):
        self.assertEqual(cc.check_range(self.root, self.base, self.base), [])

    def test_a_merge_commit_is_refused(self):
        git(self.root, "checkout", "-q", "-b", "side")
        commit(self.root, b"feat: on a side branch")
        git(self.root, "checkout", "-q", "main")
        commit(self.root, b"fix: on main")
        git(self.root, "merge", "-q", "--no-ff", "-m", "Merge branch 'side'", "side")

        found = cc.check_range(self.root, self.base, git(self.root, "rev-parse", "HEAD"))

        self.assertTrue(has(found, "merge"), found)

    def test_a_message_that_is_not_utf8_is_refused(self):
        # git commit converts such a message, so the commit object is made by hand.
        tree = git(self.root, "rev-parse", "HEAD^{tree}")
        who = b"A <a@example.com> 0 +0000"
        body = b"tree " + tree.encode() + b"\nparent " + self.base.encode() + b"\nauthor " + who
        body += b"\ncommitter " + who + b"\n\nfeat: caf\xe9 au lait\n"

        bad = subprocess.run(
            ["git", "-C", str(self.root), "hash-object", "-t", "commit", "-w", "--literally", "--stdin"],
            input=body,
            capture_output=True,
            check=True,
            env={**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"},
        ).stdout.decode().strip()

        found = cc.check_range(self.root, self.base, bad)

        self.assertTrue(has(found, bad[:12], "UTF-8"), found)

    def test_hostile_messages_are_reported_without_their_text(self):
        commit(self.root, f"feat: add it\x1b[2J\n\n{CANARY}\u202e forged: line\n".encode())
        commit(self.root, f"{CANARY}\nfeat: add it".encode())

        found = cc.check_range(self.root, self.base, git(self.root, "rev-parse", "HEAD"))

        self.assertTrue(found)

        for problem in found:
            self.assertNotIn("canary", problem)
            self.assertTrue(problem.isprintable(), repr(problem))

    def test_a_message_over_the_limit_is_refused(self):
        big = commit(self.root, b"feat: add it\n\n" + b"x" * (cc.MAX_MESSAGE + 1))

        self.assertTrue(has(cc.check_range(self.root, self.base, big), big[:12], "long"))

    def test_more_commits_than_the_limit_are_refused(self):
        for i in range(5):
            commit(self.root, f"feat: change {i}".encode())

        head = git(self.root, "rev-parse", "HEAD")

        old, cc.MAX_COMMITS = cc.MAX_COMMITS, 3

        try:
            found = cc.check_range(self.root, self.base, head)
        finally:
            cc.MAX_COMMITS = old

        self.assertTrue(has(found, "more than"), found)

    def test_a_reference_is_never_taken_for_an_id(self):
        head = git(self.root, "rev-parse", "HEAD")
        target = self.root / "pwned"

        for bad in [
            "HEAD",
            "main",
            "HEAD~1",
            "--output=" + str(target),
            "-x",
            "--all",
            "",
            " ",
            head + " ",
            head[:7],
            head.upper(),
            "g" * 40,
            "0" * 39,
            "0" * 41,
            head + "\n" + head,
            head + "..",
            head + "^",
            "@{0}",
        ]:
            for args in [(bad, head), (head, bad)]:
                with self.subTest(args):
                    with self.assertRaises(cc.CheckError):
                        cc.check_range(self.root, *args)

        self.assertFalse(target.exists())

    def test_an_id_that_is_not_in_the_repository_is_an_error_not_a_pass(self):
        with self.assertRaises(cc.CheckError):
            cc.check_range(self.root, self.base, "0" * 40)

        with self.assertRaises(cc.CheckError):
            cc.check_range(self.root, "0" * 40, self.base)

    def test_a_directory_that_is_not_a_repository_is_an_error(self):
        with tempfile.TemporaryDirectory() as empty:
            with self.assertRaises(cc.CheckError):
                cc.check_range(pathlib.Path(empty), self.base, self.base)

    def test_the_errors_do_not_repeat_what_they_were_given(self):
        try:
            cc.check_range(self.root, f"--{CANARY}", self.base)
        except cc.CheckError as err:
            self.assertNotIn("canary", str(err))
        else:
            self.fail("an option was taken for an id")


class CommandLine(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)

        self.root = pathlib.Path(tmp.name)

        git(self.root, "init", "-q")

        self.base = commit(self.root, b"chore: init")

    def run_main(self, *argv, env=None):
        out, err = io.StringIO(), io.StringIO()

        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cc.main(list(argv), env or {})

        return code, out.getvalue(), err.getvalue()

    def args(self, head):
        return ["--root", str(self.root), "--base", self.base, "--head", head]

    def test_good_commits_exit_0(self):
        head = commit(self.root, b"feat: add a thing")

        code, out, err = self.run_main(*self.args(head))

        self.assertEqual(code, 0, out + err)

    def test_a_bad_commit_exits_1_and_says_which(self):
        head = commit(self.root, f"Added {CANARY}.".encode())

        code, out, err = self.run_main(*self.args(head))

        self.assertEqual(code, 1)
        self.assertIn(head[:12], out + err)
        self.assertNotIn("canary", out + err)

    def test_the_title_is_read_from_the_environment(self):
        head = commit(self.root, b"feat: add a thing")

        self.assertEqual(self.run_main(*self.args(head), env={"PR_TITLE": "feat: add the framework"})[0], 0)

        code, out, err = self.run_main(*self.args(head), env={"PR_TITLE": f"Add {CANARY}."})

        self.assertEqual(code, 1)
        self.assertIn("title", out + err)
        self.assertNotIn("canary", out + err)

    def test_an_empty_title_is_a_mistake_but_no_title_is_not(self):
        head = commit(self.root, b"feat: add a thing")

        self.assertEqual(self.run_main(*self.args(head), env={"PR_TITLE": ""})[0], 1)
        self.assertEqual(self.run_main(*self.args(head), env={})[0], 0)

    def test_a_check_that_cannot_be_made_exits_2(self):
        head = commit(self.root, b"feat: add a thing")

        for argv in [
            [],
            ["--base", self.base],
            ["--head", head],
            ["--root", str(self.root), "--base", "HEAD", "--head", head],
            ["--root", str(self.root), "--base", self.base, "--head", "0" * 40],
            [*self.args(head), "--unknown"],
            [*self.args(head), "extra"],
            ["--root", str(self.root), "--base", self.base, "--base", self.base, "--head", head],
        ]:
            with self.subTest(argv):
                code, out, err = self.run_main(*argv)

                self.assertEqual(code, 2, out + err)
                self.assertTrue(err.strip())

    def test_the_commits_of_a_branch_are_checked_from_the_merge_base(self):
        # What `make check-commits` does: from where the branch left main.
        git(self.root, "checkout", "-q", "-b", "work")
        commit(self.root, b"feat: add a thing")
        head = commit(self.root, b"fix: stop a leak")

        self.assertEqual(self.run_main("--root", str(self.root), "--base", self.base, "--head", head)[0], 0)


class WiredIn(unittest.TestCase):
    def job(self) -> str:
        text = (REPO / ".github/workflows/ci.yml").read_text()

        match = re.search(r"^  commits:\n(.*?)(?=^  [a-z][a-z0-9-]*:\n|\Z)", text, re.S | re.M)

        self.assertIsNotNone(match, "ci.yml has no `commits` job")

        return match.group(1)

    def test_it_runs_for_pull_requests_only(self):
        job = self.job()

        self.assertIn("github.event_name == 'pull_request'", job)

    def test_it_is_a_job_with_the_usual_care(self):
        job = self.job()

        self.assertIn("timeout-minutes:", job)
        self.assertRegex(job, r"permissions:\n\s+contents: read\n")
        self.assertIn("persist-credentials: false", job)
        self.assertIn("fetch-depth: 0", job)
        self.assertRegex(job, r"uses: actions/checkout@[0-9a-f]{40} # v")

    def test_the_text_of_a_pull_request_reaches_it_only_through_the_environment(self):
        job = self.job()

        for line in re.findall(r"^\s+run:.*$", job, re.M):
            self.assertNotIn("${{", line)

        for name, value in {
            "BASE": "github.event.pull_request.base.sha",
            "HEAD": "github.event.pull_request.head.sha",
            "PR_TITLE": "github.event.pull_request.title",
        }.items():
            self.assertRegex(job, rf"{name}: \$\{{\{{ {re.escape(value)} \}}\}}")

        self.assertIn("check_commits.py", job)

    def test_no_workflow_runs_on_the_trusted_side_of_a_pull_request(self):
        for path in (REPO / ".github/workflows").glob("*.yml"):
            with self.subTest(path.name):
                self.assertNotIn("pull_request_target", path.read_text())

    def test_the_makefile_can_check_a_branch(self):
        text = (REPO / "Makefile").read_text()

        self.assertRegex(text, r"(?m)^\.PHONY: check-commits$")
        self.assertRegex(text, r"(?m)^check-commits:")
        self.assertIn("check_commits.py", text)
        self.assertIn("merge-base", text)

    def test_release_please_names_its_own_commits_in_the_form(self):
        config = json.loads((REPO / ".release/config.json").read_text())

        pattern = config.get("pull-request-title-pattern")

        self.assertIsNotNone(pattern, "release-please's default title has a scope that is not in the list")

        title = pattern.replace("${scope}", "").replace("${component}", " foundation").replace("${version}", "0.1.0")

        self.assertEqual(cc.check_title(title), [], title)

    def test_the_pull_request_template_has_what_the_guidelines_ask_for(self):
        text = (REPO / ".github/PULL_REQUEST_TEMPLATE.md").read_text()

        for heading in ["What", "Why", "How it was verified", "Security", "Review"]:
            self.assertRegex(text, rf"(?m)^## {heading}\b", heading)

    def test_the_guidelines_are_in_the_docs_the_agents_read(self):
        claude = (REPO / "CLAUDE.md").read_text()

        self.assertRegex(claude, r"(?m)^## Pull requests$")
        self.assertRegex(claude, r"(?m)^\| `ci\.yml` / `commits` \|")
        self.assertIn("check_commits.py", claude)
        self.assertIn("CLAUDE.md", (REPO / "AGENTS.md").read_text())

    def test_the_checker_is_in_the_tests_that_make_test_scripts_runs(self):
        # `python3 -m unittest discover -s .github/scripts` finds test_*.py.
        self.assertTrue((REPO / ".github/scripts/test_check_commits.py").is_file())
        self.assertIn("unittest discover", (REPO / "Makefile").read_text())


if __name__ == "__main__":
    unittest.main()
