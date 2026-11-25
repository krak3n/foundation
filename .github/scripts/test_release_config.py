"""Tests for the release configuration. Run: python3 -m unittest discover -s .github/scripts

A release tags modules, and a tag can't be taken back once the Go proxy has seen it, so what
is released is checked as data before anything runs: which modules there are, how each is
tagged, and that nothing refers to a module that is not in the repository. Today that is the
core module and the provider modules, config/yaml and config/etcd, which are listed once, in
the Makefile (MODULES), and every other place that has to know them is checked against it. The
OpenTelemetry modules are not committed yet, so nothing may mention them; they come back with
their code.
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

# The modules that are released apart from the core, and that the core's release moves.
MODULE_DIRS = ["config/yaml", "config/etcd"]


def makefile_modules() -> list[str]:
    """MODULES from the Makefile, which is where the modules are listed."""
    found = re.search(r"(?m)^MODULES \?= (.*)$", (REPO / "Makefile").read_text())

    assert found, "the Makefile has no MODULES"

    return found.group(1).split()


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
    def test_the_release_is_the_core_and_the_provider_modules(self):
        self.assertEqual(set(CONFIG["packages"]), {".", *MODULE_DIRS})

    def test_the_manifest_has_the_same_components(self):
        self.assertEqual(set(MANIFEST), set(CONFIG["packages"]))

    def test_every_component_is_a_module_in_its_own_directory(self):
        for path in CONFIG["packages"]:
            with self.subTest(path):
                go_mod = (REPO / path / "go.mod").read_text()
                want = MODULE if path == "." else f"{MODULE}/{path}"

                self.assertRegex(go_mod, rf"(?m)^module {re.escape(want)}$")

    def test_a_provider_module_is_tagged_with_its_directory(self):
        # Go finds a module in a subdirectory by a tag of the form config/yaml/v0.1.0.
        for path in MODULE_DIRS:
            with self.subTest(path):
                package = CONFIG["packages"].get(path, {})

                self.assertIs(package.get("include-component-in-tag"), True)
                self.assertEqual(package.get("tag-separator"), "/")
                self.assertEqual(package.get("release-type"), "go")
                self.assertEqual(package.get("component"), path)
                self.assertEqual(package.get("changelog-path"), "CHANGELOG.md")

    def test_the_core_is_tagged_plainly(self):
        core = CONFIG["packages"]["."]

        self.assertIs(core.get("include-component-in-tag"), False)
        self.assertEqual(core.get("release-type"), "go")

    def test_a_change_to_a_provider_is_not_a_core_release(self):
        # The parser or the client can have an advisory that is fixed without a release of
        # the core, and the core has no reason to be released for a change to either.
        for path in MODULE_DIRS:
            with self.subTest(path):
                self.assertIn(path, CONFIG["packages"]["."].get("exclude-paths", []))

    def test_a_change_to_one_provider_is_not_a_release_of_another(self):
        for outer in MODULE_DIRS:
            self.assertEqual(CONFIG["packages"][outer].get("exclude-paths", []), [], outer)

    def test_the_first_release_of_each_is_0_1_0(self):
        # With no earlier tag release-please starts at initial-version, whose default is
        # 1.0.0, whatever the manifest says. The first release of a pre-1.0 module is 0.1.0.
        for path, package in CONFIG["packages"].items():
            with self.subTest(path):
                self.assertEqual(package.get("initial-version"), "0.1.0")

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

    def test_there_are_the_core_and_a_set_for_each_provider(self):
        self.assertEqual(set(self.sets), {"core", "yaml", "etcd"})

    def test_each_set_has_the_modules_of_its_component(self):
        self.assertEqual(self.sets["core"]["modules"], [MODULE])
        self.assertEqual(self.sets["yaml"]["modules"], [f"{MODULE}/config/yaml"])
        self.assertEqual(self.sets["etcd"]["modules"], [f"{MODULE}/config/etcd"])

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
    """The cascade moves the provider modules to the core that was just released. It is a
    script that is tested on its own (test-cascade.sh), and the workflow only calls it: the
    tag and the modules are read from the environment, and no tool is installed that could
    not be pinned."""

    def test_it_calls_the_script_with_the_tag_in_the_environment(self):
        self.assertIn("bash .github/scripts/cascade.sh", CASCADE)
        self.assertRegex(CASCADE, r"(?m)^\s+TAG: \$\{\{ github\.event\.release\.tag_name \|\| inputs\.tag \}\}")

    def test_it_gives_the_script_the_modules_the_makefile_lists(self):
        found = re.search(r'(?m)^\s+MODULES: "?([^"\n]+?)"?\s*$', CASCADE)

        self.assertIsNotNone(found, "the workflow does not say which modules to move")
        self.assertEqual(found.group(1).split(), makefile_modules())

    def test_the_script_exists_and_is_tested(self):
        self.assertTrue((REPO / ".github/scripts/cascade.sh").is_file())
        self.assertTrue((REPO / ".github/scripts/test-cascade.sh").is_file())
        self.assertIn("test-cascade.sh", (REPO / "Makefile").read_text())

    def test_the_yaml_only_script_is_gone(self):
        self.assertFalse((REPO / ".github/scripts/cascade-yaml.sh").exists())
        self.assertFalse((REPO / ".github/scripts/test-cascade-yaml.sh").exists())

        for name in ["Makefile", "CLAUDE.md", "SECURITY.md", ".github/workflows/release-cascade.yml"]:
            self.assertNotIn("cascade-yaml", (REPO / name).read_text(), name)

    def test_it_does_not_use_multimod(self):
        # It can't be installed with go install (its go.mod has a replace directive), its
        # flags are not the ones the workflow used, and it works on a clean tree on a branch
        # of its own, before a tag exists. Go's own tools do what is needed.
        for name in [".github/workflows/release-cascade.yml", ".github/scripts/cascade.sh", "Makefile"]:
            self.assertNotIn("multimod", (REPO / name).read_text().lower(), name)

    def test_it_installs_nothing(self):
        self.assertNotRegex(CASCADE, r"go (install|run) ")

    def test_it_says_which_module_it_is_for(self):
        self.assertNotRegex(without_tools(CASCADE), r"(?i)otel|telemetry", "it still talks about OpenTelemetry")
        self.assertIn("modules", CASCADE)
        self.assertNotIn("yaml module", CASCADE)

    def test_the_script_names_no_module_itself(self):
        # The list comes in, so adding a module is a change to the list and not to the script.
        # Its comments say how it is used, with examples; what it does names none.
        text = "\n".join(l for l in (REPO / ".github/scripts/cascade.sh").read_text().splitlines() if not l.lstrip().startswith("#"))

        for path in MODULE_DIRS:
            self.assertNotIn(path, text, "a module is named in the script")
        self.assertIn("set-core-version.sh", text)
        self.assertNotRegex(text, r"(?i)telemetry|otel")


class Workspace(unittest.TestCase):
    """The yaml module is tested against the code in this checkout, not a release."""

    def test_the_workspace_does_not_pin_a_version_of_the_core(self):
        makefile = (REPO / "Makefile").read_text()
        recipe = re.search(r"(?ms)^workspace:\n(.*?)(?=^\S)", makefile)

        self.assertIsNotNone(recipe)
        self.assertNotRegex(recipe.group(1), r"@v\d", "the replace was for the unreleased core, v0.0.0")


class YamlRetraction(unittest.TestCase):
    """config/yaml/v0.1.0 was released before the cascade had run, so it requires a core
    version that does not exist. The tag can't be removed (the Go proxy has it), so the next
    version retracts it, and nothing else."""

    def gomod(self) -> str:
        return (REPO / "config" / "yaml" / "go.mod").read_text()

    def test_v0_1_0_is_retracted_with_a_reason_and_nothing_else_is(self):
        retracts = [l.strip() for l in self.gomod().splitlines() if re.match(r"\s*retract\b", l)]

        self.assertEqual(len(retracts), 1, retracts)
        self.assertRegex(retracts[0], r"^retract v0\.1\.0 // \S.{10,}$", "retract v0.1.0 and say why")

    def test_the_reason_has_no_data_in_it(self):
        # Tooling shows it to everyone who asks for the versions.
        reason = re.search(r"(?m)^retract v0\.1\.0 // (.*)$", self.gomod())

        self.assertIsNotNone(reason)
        self.assertRegex(reason.group(1), r"^[A-Za-z0-9 .,;:()'-]+$")

    def test_it_requires_a_core_that_exists(self):
        core = re.search(r"(?m)^\s*go\.krak3n\.dev/foundation (v\S+)", self.gomod())

        self.assertIsNotNone(core)
        self.assertNotEqual(core.group(1), "v0.0.0", "no such release of the core")


class EveryProviderRequiresACoreThatExists(unittest.TestCase):
    """The first release of a provider is made before the cascade can run, so what it
    requires when it is made is what it is released with: a core that has been released.
    (That is how config/yaml/v0.1.0 came to require v0.0.0 and had to be retracted.)"""

    def test_none_requires_the_core_at_v0_0_0(self):
        for path in MODULE_DIRS:
            with self.subTest(path):
                text = (REPO / path / "go.mod").read_text()
                core = re.search(r"(?m)^(?:require\s+)?\s*go\.krak3n\.dev/foundation (v\S+)", text)

                self.assertIsNotNone(core, f"{path} does not require the core")
                self.assertNotEqual(core.group(1), "v0.0.0", "no such release of the core")

    def test_the_new_provider_has_nothing_to_retract(self):
        text = (REPO / "config/etcd/go.mod").read_text()

        self.assertNotRegex(text, r"(?m)^\s*retract\b")

    def test_every_provider_has_code_that_uses_the_core(self):
        # go mod tidy drops a requirement nothing imports, and the cascade would then have
        # nothing to move.
        for path in MODULE_DIRS:
            with self.subTest(path):
                sources = [p for p in (REPO / path).glob("*.go") if not p.name.endswith("_test.go")]

                self.assertTrue(any('"go.krak3n.dev/foundation' in p.read_text() for p in sources))


class ModulesAgree(unittest.TestCase):
    """A module is listed once, in the Makefile, and everything else that has to know about it
    is checked against that list, so a module added or removed in one place and forgotten in
    another is red here and not found at a release."""

    def test_the_makefile_lists_the_providers(self):
        self.assertEqual(makefile_modules(), MODULE_DIRS)

    def test_the_release_components_are_the_core_and_those_modules(self):
        self.assertEqual(set(CONFIG["packages"]) - {"."}, set(makefile_modules()))
        self.assertEqual(set(MANIFEST) - {"."}, set(makefile_modules()))

    def test_the_module_sets_are_the_core_and_those_modules(self):
        sets = module_sets(VERSIONS)
        listed = {m for name, s in sets.items() if name != "core" for m in s["modules"]}

        self.assertEqual(listed, {f"{MODULE}/{d}" for d in makefile_modules()})

    def test_each_module_is_in_a_set_of_its_own(self):
        sets = module_sets(VERSIONS)

        for name, s in sets.items():
            if name != "core":
                self.assertEqual(len(s["modules"]), 1, name)

    def test_dependabot_watches_each_module_and_leaves_the_core_to_the_cascade(self):
        text = (REPO / ".github/dependabot.yml").read_text()
        entries = re.split(r"(?m)^  - package-ecosystem: ", text)[1:]
        gomod = {}

        for entry in entries:
            if entry.startswith("gomod"):
                directory = re.search(r"(?m)^    directory: (\S+)", entry).group(1)
                gomod[directory] = entry

        self.assertEqual(set(gomod), {"/"} | {"/" + d for d in makefile_modules()})

        for directory, entry in gomod.items():
            if directory != "/":
                with self.subTest(directory):
                    # The core's version here is the cascade's to move, not Dependabot's.
                    self.assertRegex(entry, r"(?m)^\s+- dependency-name: go\.krak3n\.dev/foundation\s*$")
                    self.assertRegex(entry, r"(?m)^    cooldown:")

    def test_govulncheck_runs_in_each_module(self):
        text = (REPO / ".github/workflows/security.yml").read_text()
        job = re.search(r"(?ms)^  govulncheck:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)", text).group(1)

        self.assertEqual(re.findall(r"(?m)^\s+working-directory: (\S+)\s*$", job), makefile_modules())

    def test_the_workflows_do_not_name_the_modules(self):
        # They go through the Makefile (workspace, vet, test, staticcheck, tidy-check), so a
        # module added to MODULES is built, vetted, tested and checked without a change here.
        text = (REPO / ".github/workflows/ci.yml").read_text()

        for path in makefile_modules():
            self.assertNotIn(path, text, path)

    def test_every_make_target_that_looks_at_code_goes_through_the_list(self):
        makefile = (REPO / "Makefile").read_text()

        for target in ["workspace", "tidy", "tidy-check", "vet", "staticcheck", "test"]:
            recipe = re.search(rf"(?ms)^{re.escape(target)}:[^\n]*\n(.*?)(?=^\S|\Z)", makefile)

            self.assertIsNotNone(recipe, target)
            self.assertIn("$(MODULES)", recipe.group(1), target)

    def test_the_documents_list_each_module(self):
        claude = (REPO / "CLAUDE.md").read_text()
        table = claude[claude.index("### Module layout") :]
        table = table[: table.index("### Key packages")]

        for path in makefile_modules():
            with self.subTest(path):
                self.assertIn(f"`{MODULE}/{path}` | `{path}/go.mod`", table)

        self.assertRegex(table, r"Module sets \(`core`, `yaml`, `etcd`\)")
        self.assertIn("three components", table)

    def test_every_module_has_a_go_mod_that_names_itself(self):
        for path in makefile_modules():
            self.assertRegex((REPO / path / "go.mod").read_text(), rf"(?m)^module {re.escape(MODULE + '/' + path)}$")


class Tidy(unittest.TestCase):
    """make tidy and make tidy-check look at the modules we release and nothing else:
    a go.mod in an untracked directory (a local stub that needs an unreleased core) is not
    ours to touch, and a local go.work must not change the answer."""

    def recipe(self, target: str) -> str:
        found = re.search(rf"(?ms)^{re.escape(target)}:[^\n]*\n(.*?)(?=^\S|\Z)", (REPO / "Makefile").read_text())

        self.assertIsNotNone(found, f"no {target} target")

        return found.group(1)

    def test_tidy_goes_through_the_modules_we_release(self):
        recipe = self.recipe("tidy")

        self.assertNotIn("find ", recipe, "find walks into directories that are not ours")
        self.assertIn("$(MODULES)", recipe)
        self.assertIn("GOWORK=off", recipe)
        self.assertIn("go mod tidy", recipe)

    def test_tidy_check_only_reads(self):
        recipe = self.recipe("tidy-check")

        self.assertNotIn("find ", recipe)
        self.assertIn("$(MODULES)", recipe)
        self.assertIn("GOWORK=off", recipe)
        self.assertRegex(recipe, r"go mod tidy -diff")
        self.assertNotRegex(recipe, r"go mod tidy(?! -diff)", "a check must not change files")

    def test_both_are_phony(self):
        makefile = (REPO / "Makefile").read_text()

        self.assertRegex(makefile, r"(?m)^\.PHONY: tidy$")
        self.assertRegex(makefile, r"(?m)^\.PHONY: tidy-check$")

    def test_ci_runs_the_check_in_the_tooling_job(self):
        ci = (REPO / ".github" / "workflows" / "ci.yml").read_text()
        job = re.search(r"(?ms)^  tooling:\n(.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)", ci)

        self.assertIsNotNone(job)
        self.assertRegex(job.group(1), r"(?m)^\s+run: make tidy-check\s*$")

    def test_the_check_is_in_the_ci_table(self):
        claude = (REPO / "CLAUDE.md").read_text()
        row = next((l for l in claude.splitlines() if l.startswith("| `ci.yml` / `tooling`")), "")

        self.assertIn("make tidy-check", row)


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
        ".github/scripts/cascade.sh",
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
