"""Tests for the release configuration. Run: python3 -m unittest discover -s .github/scripts

A release tags modules, and a tag can't be taken back once the Go proxy has seen it, so what
is released is checked as data before anything runs: which modules there are, how each is
tagged, and that nothing refers to a module that is not in the repository. Today that is the
core module and config/yaml. The OpenTelemetry modules are not committed yet, so nothing may
mention them; they come back with their code.
"""

import json
import pathlib
import re
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
MODULE = "go.krak3n.dev/foundation"

CONFIG = json.loads((REPO / ".release/config.json").read_text())
MANIFEST = json.loads((REPO / ".release/manifest.json").read_text())
VERSIONS = (REPO / ".release/versions.yaml").read_text()
CASCADE = (REPO / ".github/workflows/release-cascade.yml").read_text()


def module_sets(text: str) -> dict[str, dict]:
    """versions.yaml as {set: {"version": v, "modules": [...]}}. It is read as text: the
    file is a fixed shape and the checks need nothing installed."""
    sets: dict[str, dict] = {}
    current = None
    in_modules = False

    for line in text.splitlines():
        if re.match(r"^module-sets:\s*$", line):
            continue

        if m := re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line):
            current = sets.setdefault(m.group(1), {"version": None, "modules": []})
            in_modules = False
        elif current is not None and (m := re.match(r"^    version:\s*(\S+)\s*$", line)):
            current["version"] = m.group(1)
        elif current is not None and re.match(r"^    modules:\s*$", line):
            in_modules = True
        elif current is not None and in_modules and (m := re.match(r"^      - (\S+)\s*$", line)):
            current["modules"].append(m.group(1))
        elif re.match(r"^\S", line):
            current = None

    return sets


def without_tools(text: str) -> str:
    """text with the name of OpenTelemetry's build tools taken out: multimod and crosslink
    are used as tools and stay, which is not the same as having its modules."""
    return text.replace("go.opentelemetry.io/build-tools", "").replace("opentelemetry-go-build-tools", "")


def directory(module: str) -> pathlib.Path:
    assert module == MODULE or module.startswith(MODULE + "/"), module

    return REPO / module.removeprefix(MODULE).lstrip("/")


class Components(unittest.TestCase):
    def test_the_release_is_the_core_and_the_yaml_module(self):
        self.assertEqual(set(CONFIG["packages"]), {".", "config/yaml"})

    def test_the_manifest_has_the_same_components(self):
        self.assertEqual(set(MANIFEST), set(CONFIG["packages"]))

    def test_every_component_is_a_module_in_its_own_directory(self):
        for path in CONFIG["packages"]:
            with self.subTest(path):
                go_mod = (REPO / path / "go.mod").read_text()
                want = MODULE if path == "." else f"{MODULE}/{path}"

                self.assertRegex(go_mod, rf"(?m)^module {re.escape(want)}$")

    def test_the_yaml_module_is_tagged_with_its_directory(self):
        # Go finds a module in a subdirectory by a tag of the form config/yaml/v0.1.0.
        yaml = CONFIG["packages"].get("config/yaml", {})

        self.assertIs(yaml.get("include-component-in-tag"), True)
        self.assertEqual(yaml.get("tag-separator"), "/")
        self.assertEqual(yaml.get("release-type"), "go")

    def test_the_core_is_tagged_plainly(self):
        core = CONFIG["packages"]["."]

        self.assertIs(core.get("include-component-in-tag"), False)
        self.assertEqual(core.get("release-type"), "go")

    def test_a_change_to_yaml_is_not_a_core_release(self):
        # The parser can have an advisory that is fixed without a release of the core.
        self.assertIn("config/yaml", CONFIG["packages"]["."].get("exclude-paths", []))

    def test_the_components_are_not_linked(self):
        for plugin in CONFIG.get("plugins", []):
            self.assertNotEqual(plugin.get("type"), "linked-versions", plugin)

    def test_no_component_is_inside_another_without_being_excluded(self):
        for outer in CONFIG["packages"]:
            for inner in CONFIG["packages"]:
                if outer != inner and (outer == "." or inner.startswith(outer + "/")):
                    self.assertIn(inner, CONFIG["packages"][outer].get("exclude-paths", []), (outer, inner))

    def test_the_files_a_release_rewrites_are_there_and_marked(self):
        for path, package in CONFIG["packages"].items():
            for extra in package.get("extra-files", []):
                with self.subTest(path=path, extra=extra):
                    text = (REPO / path / extra).read_text()

                    self.assertIn("x-release-please-version", text)


class ModuleSets(unittest.TestCase):
    sets = module_sets(VERSIONS)

    def test_there_are_the_core_and_the_yaml_sets(self):
        self.assertEqual(set(self.sets), {"core", "yaml"})

    def test_each_set_has_the_modules_of_its_component(self):
        self.assertEqual(self.sets["core"]["modules"], [MODULE])
        self.assertEqual(self.sets["yaml"]["modules"], [f"{MODULE}/config/yaml"])

    def test_every_module_a_set_names_is_in_the_repository(self):
        for name, s in self.sets.items():
            for module in s["modules"]:
                with self.subTest(name=name, module=module):
                    go_mod = (directory(module) / "go.mod").read_text()

                    self.assertRegex(go_mod, rf"(?m)^module {re.escape(module)}$")

    def test_every_module_in_the_repository_is_in_a_set(self):
        named = {m for s in self.sets.values() for m in s["modules"]}
        found = set()

        for go_mod in REPO.rglob("go.mod"):
            parts = go_mod.relative_to(REPO).parts

            if "testdata" in parts or parts[0].startswith(".") or parts[0] == "telemetry":
                continue

            found.add(re.search(r"(?m)^module (\S+)$", go_mod.read_text()).group(1))

        self.assertEqual(found, named)

    def test_a_set_has_a_version_that_is_a_version(self):
        for name, s in self.sets.items():
            self.assertRegex(s["version"] or "", r"^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$", name)


class Cascade(unittest.TestCase):
    """The cascade moves the yaml module to the core that was just released. It is a script
    that is tested on its own (test-cascade-yaml.sh), and the workflow only calls it: the
    tag is read from the environment, and no tool is installed that could not be pinned."""

    def test_it_calls_the_script_with_the_tag_in_the_environment(self):
        self.assertIn("bash .github/scripts/cascade-yaml.sh", CASCADE)
        self.assertRegex(CASCADE, r"(?m)^\s+TAG: \$\{\{ github\.event\.release\.tag_name \|\| inputs\.tag \}\}")

    def test_the_script_exists_and_is_tested(self):
        self.assertTrue((REPO / ".github/scripts/cascade-yaml.sh").is_file())
        self.assertTrue((REPO / ".github/scripts/test-cascade-yaml.sh").is_file())
        self.assertIn("test-cascade-yaml.sh", (REPO / "Makefile").read_text())

    def test_it_does_not_use_multimod(self):
        # It can't be installed with go install (its go.mod has a replace directive), its
        # flags are not the ones the workflow used, and it works on a clean tree on a branch
        # of its own, before a tag exists. Go's own tools do what is needed.
        for name in [".github/workflows/release-cascade.yml", ".github/scripts/cascade-yaml.sh", "Makefile"]:
            self.assertNotIn("multimod", (REPO / name).read_text().lower(), name)

    def test_it_installs_nothing(self):
        self.assertNotRegex(CASCADE, r"go (install|run) ")

    def test_it_says_which_module_it_is_for(self):
        self.assertNotRegex(without_tools(CASCADE), r"(?i)otel|telemetry", "it still talks about OpenTelemetry")
        self.assertIn("yaml module", CASCADE)

    def test_the_script_only_touches_the_yaml_module_and_the_versions_file(self):
        text = (REPO / ".github/scripts/cascade-yaml.sh").read_text()

        self.assertIn("config/yaml", text)
        self.assertIn("set-core-version.sh", text)
        self.assertNotRegex(text, r"(?i)telemetry|otel")


class NoMultimod(unittest.TestCase):
    def test_the_makefile_has_no_targets_that_need_it(self):
        makefile = (REPO / "Makefile").read_text()

        self.assertNotIn("MULTIMOD_VERSION", makefile)
        self.assertNotIn("CROSSLINK_VERSION", makefile)

        # crosslink can't be installed either, and nothing needs it: the workspace is
        # made by make workspace.
        for target in ["tools", "crosslink", "work"]:
            self.assertNotRegex(makefile, rf"(?m)^{target}:", target)

        for target in ["prerelease", "tag"]:
            self.assertNotRegex(makefile, rf"(?m)^{target}:", target)

    def test_the_docs_do_not_send_anyone_to_it(self):
        claude = (REPO / "CLAUDE.md").read_text()

        self.assertNotIn("MULTIMOD_VERSION", claude)
        self.assertNotRegex(claude, r"(?m)^make (tools|work|crosslink)\b")
        self.assertNotRegex(claude, r"multimod prerelease")


class NoTelemetry(unittest.TestCase):
    """The OpenTelemetry modules are not in the repository yet."""

    FILES = [
        ".release/config.json",
        ".release/manifest.json",
        ".release/versions.yaml",
        ".github/workflows/release-cascade.yml",
        ".github/scripts/cascade-yaml.sh",
        ".github/workflows/release-please.yml",
        ".github/workflows/ci.yml",
        ".github/workflows/security.yml",
        ".github/dependabot.yml",
        "Makefile",
    ]

    def test_nothing_that_releases_or_builds_mentions_them(self):
        for name in self.FILES:
            text = without_tools((REPO / name).read_text())

            with self.subTest(name):
                self.assertNotRegex(text, r"(?i)telemetry")
                # The tools are OpenTelemetry's and stay; a module set of that name goes.
                self.assertNotRegex(text, r"(?m)MODSET=otel|-s=otel|^\s+otel:")

    def test_they_cannot_be_committed_by_accident(self):
        lines = [l.strip() for l in (REPO / ".gitignore").read_text().splitlines()]

        self.assertIn("/telemetry/", lines)


if __name__ == "__main__":
    unittest.main()
