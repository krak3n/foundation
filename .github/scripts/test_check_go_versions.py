"""Tests for check_go_versions.py. Run: python3 -m unittest discover -s .github/scripts

The checker keeps the `go` line of every go.mod at the oldest Go version the project
supports, which is the first release of the one before the current stable release
(`go 1.26.0`, the form `go mod init` writes, so a dependency that says `1.26.0` never
moves it): a lower line promises
what CI doesn't test, a higher one stops the older release from building the module
at all (a toolchain would be downloaded in its place, and the test of the older
release would be a test of the newer). A go.mod comes from a pull request, so it is
read as text only and never run, and anything that can't be understood fails.

The last tests run it on the real repository.
"""

import contextlib
import io
import os
import pathlib
import random
import tempfile
import unittest

import check_go_versions as cg

REPO = pathlib.Path(__file__).resolve().parents[2]

GOOD = """\
module example.com/m

go 1.26.0

require (
	example.com/a v1.0.0
)
"""


def problems(text: str, want: str = "1.26") -> list[str]:
    return cg.check_text(text, "go.mod", want)


def with_go(version: str) -> str:
    """GOOD with another go line."""
    return GOOD.replace("go 1.26.0", "go " + version)


def has(found: list[str], *words: str) -> bool:
    return any(all(w in p for w in words) for p in found)


class ExpectedOldstable(unittest.TestCase):
    def test_it_is_the_minor_before_the_stable_release(self):
        for goversion, want in {
            "go1.27.3": "1.26",
            "go1.27": "1.26",
            "go1.27.0": "1.26",
            "go1.30.12": "1.29",
            "go1.2.1": "1.1",
        }.items():
            with self.subTest(goversion):
                self.assertEqual(cg.oldstable(goversion), want)

    def test_anything_it_cannot_read_is_an_error(self):
        for goversion in [
            "",
            " ",
            "go",
            "1.27.3",
            "go1",
            "go1.",
            "go1.x",
            "go2.0",
            "go1.0",
            "go1.0.1",
            "go1.27.3.4",
            "go1.27rc1",
            "go1.27beta1",
            "devel go1.28-abcdef Mon Jan 1 00:00:00 2029 +0000",
            "go1.27.3\n",
            "go1.27.3 ",
            " go1.27.3",
            "GO1.27.3",
            "go1.-1",
            "go1.27.-1",
            "go1.０27",
            "go01.27",
            "go1.027",
            "go1.27.03",
            "go1.27\x00",
            "go1." + "9" * 1000,
            "x" * 100000,
        ]:
            with self.subTest(goversion[:40]):
                with self.assertRaises(cg.VersionError) as ctx:
                    cg.oldstable(goversion)

                # The reason does not repeat what it was given, which may be huge or
                # may hold control characters.
                self.assertLess(len(str(ctx.exception)), 200)
                self.assertTrue(str(ctx.exception).isprintable())


class CheckText(unittest.TestCase):
    def test_the_expected_version_passes(self):
        self.assertEqual(problems(GOOD), [])

    def test_the_first_release_of_the_minor_is_the_form_required(self):
        self.assertEqual(problems(with_go("1.26.0")), [])

    def test_the_bare_minor_fails_and_names_the_form(self):
        found = problems(with_go("1.26"))

        self.assertTrue(has(found, "go.mod:3", "want 1.26.0"), found)

    def test_the_message_names_the_form_to_write(self):
        found = problems(with_go("1.25.0"))

        self.assertTrue(has(found, "go.mod:3", "want 1.26.0"), found)

    def test_other_versions_fail_and_say_which(self):
        for version in ["1.25", "1.25.0", "1.27", "1.27.0", "1.26", "1.26.1", "1.26.3", "1.26.00", "1.26.0rc1", "1.2", "1.260", "1.260.0", "2.26.0", "1.26rc1", "1.26.0-x", "99", "1", "1.26.0.0", "0.26.0"]:
            with self.subTest(version):
                found = problems(with_go(version))

                self.assertTrue(has(found, "go.mod:3", "1.26.0"), found)

    def test_the_wanted_version_is_the_argument_not_a_constant(self):
        self.assertEqual(problems(with_go("1.29.0"), want="1.29"), [])
        self.assertTrue(problems(with_go("1.29"), want="1.29"))
        self.assertTrue(problems(GOOD, want="1.29"))

    def test_a_missing_directive_fails(self):
        self.assertTrue(has(problems("module example.com/m\n"), "no go directive"))
        self.assertTrue(has(problems(""), "no go directive"))

    def test_a_repeated_directive_fails(self):
        found = problems(GOOD + "\ngo 1.26.0\n")

        self.assertTrue(has(found, "more than once"), found)

    def test_a_repeated_directive_fails_even_if_both_are_right_or_one_is_wrong(self):
        self.assertTrue(problems("go 1.26.0\ngo 1.25.0\n"))
        self.assertTrue(problems("go 1.25.0\ngo 1.26.0\n"))

    def test_a_toolchain_line_fails(self):
        for line in ["toolchain go1.26.3", "toolchain go1.99", "toolchain default", "toolchain\tgo1.26.0"]:
            with self.subTest(line):
                found = problems(GOOD + line + "\n")

                self.assertTrue(has(found, "toolchain"), found)

    def test_a_toolchain_line_fails_even_when_the_go_line_is_right(self):
        self.assertTrue(has(problems("go 1.26.0\ntoolchain go1.26.0\n"), "toolchain"))

    def test_words_that_look_like_the_directive_are_not_it(self):
        text = """\
module example.com/go

// go 1.99
// toolchain go1.99

go 1.26.0 // go 1.99

require (
	example.com/go v1.0.0 // go 1.99
	example.com/toolchain v1.0.0
)

replace example.com/a => ../go 1.99
exclude example.com/b v1.0.0
retract v1.0.0 // go 1.99
"""

        self.assertEqual(problems(text), [])

    def test_a_directive_in_a_block_is_not_a_directive(self):
        for block in ["require", "replace", "exclude", "retract", "tool", "godebug"]:
            with self.subTest(block):
                text = f"go 1.26.0\n\n{block} (\n\tgo 1.99\n\ttoolchain go1.99\n)\n"

                self.assertEqual(problems(text), [])

    def test_a_directive_after_a_closed_block_is_one(self):
        self.assertTrue(has(problems("require (\n\texample.com/a v1.0.0\n)\n\ngo 1.25.0\n"), "go.mod:5"))

    def test_the_comment_after_the_line_is_not_part_of_the_version(self):
        self.assertEqual(problems("go 1.26.0//x\n"), [])
        self.assertEqual(problems("go 1.26.0 // x\n"), [])
        self.assertEqual(problems("go\t1.26.0\t// x\n"), [])
        self.assertEqual(problems("go   1.26.0   \n"), [])
        self.assertTrue(problems("go 1.26 // 1.26.0\n"))

    def test_a_quoted_version_is_refused(self):
        # go does not take one, and a reader that did would see a different file.
        self.assertTrue(problems('go "1.26.0"\n'))
        self.assertTrue(problems("go `1.26.0`\n"))

    def test_a_line_that_runs_the_word_into_the_version_is_not_a_directive(self):
        self.assertTrue(has(problems("go1.26.0\n"), "no go directive"))

    def test_other_line_endings(self):
        self.assertEqual(problems(GOOD.replace("\n", "\r\n")), [])
        self.assertTrue(problems(with_go("1.25.0").replace("\n", "\r\n")))
        self.assertTrue(problems(with_go("1.26").replace("\n", "\r\n")))

    def test_a_missing_final_newline(self):
        self.assertEqual(problems("go 1.26.0"), [])
        self.assertTrue(problems("go 1.26"))
        self.assertTrue(problems("go 1.25.0"))

    def test_a_byte_order_mark_is_refused(self):
        self.assertTrue(has(problems("﻿" + GOOD), "byte order mark"))

    def test_a_nul_or_other_control_character_is_refused(self):
        for char in ["\x00", "\x1b", "\x7f", "‮", " "]:
            with self.subTest(repr(char)):
                found = problems(GOOD + "// " + char + "\n")

                self.assertTrue(has(found, "character"), found)

    def test_invalid_utf8_is_refused(self):
        found = cg.check_bytes(GOOD.encode() + b"// \xff\xfe\n", "go.mod", "1.26")

        self.assertTrue(has(found, "UTF-8"), found)

    def test_a_huge_file_is_refused_without_being_read_through(self):
        found = cg.check_bytes(b"go 1.26.0\n" + b"// x\n" * 300000, "go.mod", "1.26")

        self.assertTrue(has(found, "larger than"), found)

    def test_a_huge_line_is_fine_and_quick(self):
        self.assertEqual(problems("go 1.26.0\n// " + "x" * 500000 + "\n"), [])

    def test_many_lines_are_fine(self):
        self.assertEqual(problems("go 1.26.0\n" + "// x\n" * 20000), [])

    def test_deep_and_unbalanced_blocks(self):
        # A block that is never closed must not hide a directive after it forever,
        # nor make a later directive count: only a closed block hides lines.
        self.assertTrue(problems("require (\n\tx v1\ngo 1.25.0\n"))
        self.assertTrue(problems("))))\ngo 1.25.0\n"))
        self.assertEqual(problems("go 1.26.0\n" + "require (\n" * 1000), [])

    def test_a_problem_never_repeats_a_long_or_odd_value(self):
        for version in ["9" * 100000, "1.26.0\x1b[2J", "1.26.0" + "‮", "x" * 5000]:
            with self.subTest(len(version)):
                found = problems("go " + version + "\n")

                self.assertTrue(found)

                for p in found:
                    self.assertLess(len(p), 300)
                    self.assertTrue(p.isprintable(), repr(p))

    def test_each_problem_names_the_file_and_the_line(self):
        for p in cg.check_text("module m\n\ngo 1.25.0\n", "config/yaml/go.mod", "1.26"):
            self.assertTrue(p.startswith("config/yaml/go.mod:3: "), p)

    def test_checking_is_pure(self):
        before = os.environ.copy()

        problems(GOOD)

        self.assertEqual(dict(os.environ), dict(before))


class Property(unittest.TestCase):
    """A go.mod is built from pieces, and the answer is known from how it was built."""

    NOISE = [
        "// a comment",
        "// go 1.99",
        "// toolchain go1.99",
        "",
        "module example.com/m",
        "require example.com/a v1.2.3",
        "require (\n\texample.com/a v1.2.3\n\tgo 1.99\n)",
        "replace (\n\texample.com/a => ../a\n\ttoolchain go1.99\n)",
        "exclude example.com/b v0.1.0",
        "retract v1.0.0 // go 1.99",
        "godebug default=go1.21",
    ]

    def build(self, rng: random.Random, directive: str | None, extra: list[str]) -> str:
        pieces = [rng.choice(self.NOISE) for _ in range(rng.randint(0, 8))]

        for line in ([directive] if directive else []) + extra:
            pieces.insert(rng.randint(0, len(pieces)), line)

        return "\n".join(pieces) + "\n"

    def test_the_right_directive_passes_wherever_it_is(self):
        rng = random.Random(1)

        for _ in range(2000):
            text = self.build(rng, rng.choice(["go 1.26.0", "go\t1.26.0 // ok"]), [])

            self.assertEqual(problems(text), [], text)

    def test_a_wrong_directive_fails_wherever_it_is(self):
        rng = random.Random(2)

        for _ in range(2000):
            text = self.build(rng, rng.choice(["go 1.25.0", "go 1.27.0", "go 1.26", "go 1.26.1", "go 2.26.0", "go 1.2"]), [])

            self.assertTrue(problems(text), text)

    def test_no_directive_fails_wherever_the_noise_is(self):
        rng = random.Random(3)

        for _ in range(2000):
            text = self.build(rng, None, [])

            self.assertTrue(has(problems(text), "no go directive"), text)

    def test_a_toolchain_line_or_a_second_directive_fails_wherever_it_is(self):
        rng = random.Random(4)

        for _ in range(2000):
            extra = rng.choice([["toolchain go1.26.0"], ["go 1.26.0"], ["toolchain go1.99", "go 1.26.0"]])
            text = self.build(rng, "go 1.26.0", extra)

            self.assertTrue(problems(text), text)

    def test_it_never_raises(self):
        rng = random.Random(5)
        alphabet = list("go 1.26\n\t\r()/tolchain\x00\x1b‮﻿\"`") + ["go ", "toolchain ", "//", "(\n", ")\n"]

        for _ in range(3000):
            text = "".join(rng.choice(alphabet) for _ in range(rng.randint(0, 60)))

            result = problems(text)

            self.assertIsInstance(result, list)


class FindModules(unittest.TestCase):
    def tree(self, files: list[str]) -> pathlib.Path:
        root = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))

        for name in files:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(GOOD)

        return root

    def names(self, root: pathlib.Path) -> list[str]:
        return [str(p.relative_to(root)) for p in cg.find_modules(root)]

    def test_every_module_is_found_in_a_stable_order(self):
        root = self.tree(["go.mod", "config/yaml/go.mod", "telemetry/otel/traces/go.mod", "a/go.mod"])

        self.assertEqual(self.names(root), ["a/go.mod", "config/yaml/go.mod", "go.mod", "telemetry/otel/traces/go.mod"])

    def test_nothing_under_testdata_vendor_or_a_hidden_directory_counts(self):
        root = self.tree(["go.mod", "x/testdata/go.mod", "vendor/m/go.mod", "node_modules/m/go.mod", ".git/go.mod", ".cache/go.mod"])

        self.assertEqual(self.names(root), ["go.mod"])

    def test_only_files_named_go_mod_count(self):
        root = self.tree(["go.mod", "go.mod.bak", "x/go.sum", "x/go.work", "y/go.mod.txt", "z/ego.mod"])

        self.assertEqual(self.names(root), ["go.mod"])

    def test_a_directory_named_go_mod_is_not_a_module(self):
        root = self.tree(["go.mod"])
        (root / "x" / "go.mod").mkdir(parents=True)

        self.assertEqual(self.names(root), ["go.mod"])

    def test_a_symlink_is_not_followed(self):
        root = self.tree(["go.mod"])
        outside = self.tree(["go.mod", "deeper/go.mod"])

        (root / "link").symlink_to(outside, target_is_directory=True)
        (root / "file.mod").symlink_to(outside / "go.mod")
        (root / "sub").mkdir()
        (root / "sub" / "go.mod").symlink_to(outside / "go.mod")

        self.assertEqual(self.names(root), ["go.mod"])

    def test_a_symlink_loop_ends(self):
        root = self.tree(["go.mod"])
        (root / "loop").symlink_to(root, target_is_directory=True)

        self.assertEqual(self.names(root), ["go.mod"])

    def test_a_very_deep_tree_is_bounded(self):
        root = self.tree(["go.mod"])
        deep = root

        for _ in range(60):
            deep = deep / "d"

        deep.mkdir(parents=True)
        (deep / "go.mod").write_text(GOOD)

        # Deeper than any module of ours: it is refused loudly, not skipped, so that
        # a module can't be hidden by where it is.
        with self.assertRaises(cg.TreeError):
            cg.find_modules(root)

    def test_too_many_modules_is_an_error(self):
        root = self.tree([f"m{i}/go.mod" for i in range(cg.MAX_MODULES + 1)])

        with self.assertRaises(cg.TreeError):
            cg.find_modules(root)

    def test_a_missing_root_is_an_error(self):
        with self.assertRaises(cg.TreeError):
            cg.find_modules(pathlib.Path(tempfile.gettempdir()) / "does-not-exist-9d2e")


class Main(unittest.TestCase):
    def repo(self, versions: dict[str, str]) -> str:
        root = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))

        for name, version in versions.items():
            path = pathlib.Path(root, name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"module example.com/{name}\n\ngo {version}\n")

        return root

    def run_main(self, root: str, env: dict[str, str]) -> tuple[int, str]:
        out, err = io.StringIO(), io.StringIO()

        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cg.main(["--root", root], env)

        return code, out.getvalue() + err.getvalue()

    def test_every_module_at_oldstable_passes(self):
        root = self.repo({"go.mod": "1.26.0", "config/yaml/go.mod": "1.26.0"})

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 0, output)

    def test_a_module_that_says_only_the_minor_fails_and_is_named(self):
        root = self.repo({"go.mod": "1.26.0", "config/yaml/go.mod": "1.26"})

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 1)
        self.assertIn("config/yaml/go.mod:3", output)
        self.assertIn("1.26.0", output)
        self.assertNotIn("\ngo.mod:3", "\n" + output)

    def test_one_module_behind_fails_and_is_named(self):
        root = self.repo({"go.mod": "1.26.0", "config/yaml/go.mod": "1.25.0"})

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 1)
        self.assertIn("config/yaml/go.mod:3", output)
        self.assertNotIn("go.mod:3: ", output.replace("config/yaml/go.mod:3: ", ""))

    def test_a_new_release_fails_every_module_until_they_are_raised(self):
        root = self.repo({"go.mod": "1.26.0", "a/go.mod": "1.26.0"})

        code, output = self.run_main(root, {"GOVERSION": "go1.28.0"})

        self.assertEqual(code, 1)
        self.assertIn("1.27.0", output)

    def test_every_problem_is_reported_not_just_the_first(self):
        root = self.repo({"a/go.mod": "1.25.0", "b/go.mod": "1.27.0", "c/go.mod": "1.26.0"})

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 1)
        self.assertIn("a/go.mod", output)
        self.assertIn("b/go.mod", output)
        self.assertNotIn("c/go.mod", output)

    def test_no_version_in_the_environment_fails_closed(self):
        root = self.repo({"go.mod": "1.26.0"})

        for env in [{}, {"GOVERSION": ""}, {"GOVERSION": "devel go1.28-abc"}, {"GOVERSION": "go1.27rc1"}, {"GOVERSION": "x" * 100000}]:
            with self.subTest(str(env)[:40]):
                code, output = self.run_main(root, env)

                self.assertNotEqual(code, 0)
                self.assertLess(len(output), 2000)

    def test_a_root_with_no_module_fails(self):
        code, output = self.run_main(tempfile.mkdtemp(), {"GOVERSION": "go1.27.3"})

        self.assertNotEqual(code, 0)
        self.assertIn("no go.mod", output)

    def test_an_unreadable_module_fails_and_the_rest_are_still_checked(self):
        root = self.repo({"a/go.mod": "1.26.0", "b/go.mod": "1.25.0"})
        os.chmod(os.path.join(root, "a/go.mod"), 0)
        self.addCleanup(os.chmod, os.path.join(root, "a/go.mod"), 0o644)

        if os.geteuid() == 0:
            self.skipTest("root can read anything")

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 1)
        self.assertIn("a/go.mod", output)
        self.assertIn("b/go.mod", output)

    def test_a_module_that_is_a_fifo_is_refused_without_blocking(self):
        root = self.repo({"b/go.mod": "1.26.0"})
        os.mkfifo(os.path.join(root, "a.fifo"))
        os.makedirs(os.path.join(root, "a"))
        os.mkfifo(os.path.join(root, "a", "go.mod"))

        code, output = self.run_main(root, {"GOVERSION": "go1.27.3"})

        self.assertEqual(code, 1)
        self.assertIn("a/go.mod", output)

    def test_the_toolchain_is_never_run(self):
        # The environment is the only thing read: no program is started, so a
        # go.mod, however hostile, can't choose one.
        import subprocess

        calls = []
        real = subprocess.Popen

        def spy(*args, **kwargs):
            calls.append(args)

            return real(*args, **kwargs)

        subprocess.Popen = spy

        try:
            root = self.repo({"go.mod": "1.26.0"})

            self.run_main(root, {"GOVERSION": "go1.27.3"})
        finally:
            subprocess.Popen = real

        self.assertEqual(calls, [])

    def test_unknown_arguments_fail(self):
        for argv in [["--nope"], ["--root"], ["a", "b"], ["--root", "x", "--root", "y"]]:
            with self.subTest(argv):
                with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                    self.assertNotEqual(cg.main(argv, {"GOVERSION": "go1.27.3"}), 0)


class RealRepository(unittest.TestCase):
    """The modules of this repository, which is what CI checks."""

    def test_the_repository_has_the_modules_it_should(self):
        names = [str(p.relative_to(REPO)) for p in cg.find_modules(REPO)]

        for want in ["go.mod", "config/yaml/go.mod"]:
            self.assertIn(want, names)

        self.assertGreaterEqual(len(names), 2)

    def test_every_module_follows_oldstable_when_stable_is_the_version_after_it(self):
        # The line the repository promises is 1.26.0: CI passes the real stable
        # version, and this pins what the files say today.
        code = cg.main(["--root", str(REPO)], {"GOVERSION": "go1.27.0"})

        self.assertEqual(code, 0)

    def test_every_module_says_the_first_release_of_its_minor(self):
        for path in cg.find_modules(REPO):
            with self.subTest(str(path.relative_to(REPO))):
                lines = [l.split() for l in path.read_text().splitlines() if l.startswith("go ")]

                self.assertEqual(len(lines), 1, path)
                self.assertRegex(lines[0][1], r"^1\.[1-9][0-9]*\.0$")

    def test_no_module_has_a_toolchain_line(self):
        for path in cg.find_modules(REPO):
            with self.subTest(str(path.relative_to(REPO))):
                self.assertFalse(
                    any(line.split()[:1] == ["toolchain"] for line in path.read_text().splitlines()),
                    path,
                )


class Workflow(unittest.TestCase):
    """What CI runs, in the workflow, is the script and not a copy of it."""

    ci = (REPO / ".github/workflows/ci.yml").read_text()

    def test_the_check_runs_in_ci_on_the_newest_go(self):
        self.assertIn("check_go_versions.py", self.ci)
        self.assertIn("go-version: stable", self.ci)

    def test_the_tests_run_on_stable_and_oldstable(self):
        self.assertIn("[stable, oldstable]", self.ci.replace('"', ""))
        self.assertIn("fail-fast: false", self.ci)

    def test_the_go_version_is_not_pasted_into_a_script(self):
        # It reaches the script through the environment, not through ${{ }}.
        for line in self.ci.splitlines():
            if "check_go_versions.py" in line:
                self.assertNotIn("${{", line)

    def test_there_is_a_command_to_raise_every_module_at_once(self):
        self.assertRegex((REPO / "Makefile").read_text(), r"(?m)^go-version:")

    def test_the_command_writes_the_first_release_of_the_minor(self):
        # Run for real in a copy of the tree, so what it writes is what the check reads.
        import shutil
        import subprocess

        if shutil.which("go") is None or shutil.which("make") is None:
            self.skipTest("go and make are needed")

        root = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root, ignore_errors=True))

        for name in ["go.mod", "a/b/go.mod"]:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("module example.com/m\n\ngo 1.26.0\n")

        run = subprocess.run(
            ["make", "-f", str(REPO / "Makefile"), "-C", str(root), "go-version", "GO=1.27"],
            capture_output=True,
            text=True,
            env={**os.environ, "GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
        )

        self.assertEqual(run.returncode, 0, run.stderr)

        for name in ["go.mod", "a/b/go.mod"]:
            self.assertIn("\ngo 1.27.0\n", (root / name).read_text())

        self.assertEqual(cg.main(["--root", str(root)], {"GOVERSION": "go1.28.1"}), 0)

    def test_the_command_refuses_anything_but_a_release(self):
        import shutil
        import subprocess

        if shutil.which("make") is None:
            self.skipTest("make is needed")

        root = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root, ignore_errors=True))
        (root / "go.mod").write_text("module example.com/m\n\ngo 1.26.0\n")

        for value in ["", "1", "1.27.0", "1.27.1", "1.27rc1", "go1.27", "1.27; touch pwned", "$(touch pwned)", "1.27\n1.28"]:
            with self.subTest(value):
                run = subprocess.run(
                    ["make", "-f", str(REPO / "Makefile"), "-C", str(root), "go-version", "GO=" + value],
                    capture_output=True,
                    text=True,
                )

                self.assertNotEqual(run.returncode, 0)
                self.assertIn("go 1.26.0", (root / "go.mod").read_text())
                self.assertFalse((root / "pwned").exists())

    def test_no_workflow_pins_the_old_go_version_any_more(self):
        for path in (REPO / ".github/workflows").glob("*.yml"):
            with self.subTest(path.name):
                self.assertNotIn('go-version: "1.25"', path.read_text())

    def test_the_check_also_runs_on_a_schedule_so_a_new_release_is_seen_without_a_change(self):
        scheduled = [
            path.name
            for path in (REPO / ".github/workflows").glob("*.yml")
            if "check_go_versions.py" in path.read_text() and "schedule:" in path.read_text()
        ]

        self.assertTrue(scheduled)


if __name__ == "__main__":
    unittest.main()
