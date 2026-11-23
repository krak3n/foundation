# Tools are pinned to a version rather than @latest, so that what runs here and in
# the release workflow can't change under us. Go checks a tagged version against the
# checksum database, so it can't be replaced after the fact.
ACTIONLINT_VERSION ?= v1.7.12
STATICCHECK_VERSION ?= v0.8.2

# --- Development ---

# Modules other than the root that have code and tests of their own. They require
# the root module at a version that is not released yet, so they are built in a
# workspace, which is not committed. "make workspace" makes it, with nothing to
# install, and is what CI uses.
MODULES ?= config/yaml

.PHONY: workspace
workspace:
	go work init . $(addprefix ./,$(MODULES))
	go work edit -replace go.krak3n.dev/foundation@v0.0.0=.

.PHONY: tidy
tidy:
	@set -e; for dir in $$(find . -name 'go.mod' -exec dirname {} \;); do \
		echo "go mod tidy: $${dir}"; \
		(cd "$${dir}" && go mod tidy); \
	done

.PHONY: vet
vet:
	go vet ./...
	@set -e; for dir in $(MODULES); do echo "go vet: $${dir}"; (cd "$${dir}" && go vet ./...); done

# staticcheck is run from its pinned version without being added to go.mod. An
# older one can't read what a newer toolchain builds, so raise it with Go.
.PHONY: staticcheck
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...
	@set -e; for dir in $(MODULES); do echo "staticcheck: $${dir}"; (cd "$${dir}" && go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...); done

.PHONY: test
test:
	go test -race -count=1 -timeout 60s ./...
	@set -e; for dir in $(MODULES); do echo "go test: $${dir}"; (cd "$${dir}" && go test -race -count=1 -timeout 120s ./...); done

# The release tooling: the scripts that write a release tag into versions.yaml and move
# the yaml module to it, the checkers that keep the workflows and the release files from
# regressing, and the version check (see .github/scripts).
.PHONY: test-scripts
test-scripts:
	bash .github/scripts/test-set-core-version.sh
	bash .github/scripts/test-cascade-yaml.sh
	python3 -m unittest discover -s .github/scripts

# Every commit of this branch since it left main must be a conventional commit (CI runs
# the same check on a pull request). MAIN is where the branch is compared from.
MAIN ?= origin/main

.PHONY: check-commits
check-commits:
	python3 .github/scripts/check_commits.py --base "$$(git merge-base $(MAIN) HEAD)" --head "$$(git rev-parse HEAD)"

.PHONY: lint-workflows
lint-workflows:
	python3 .github/scripts/check_workflows.py
	go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION) .github/workflows/*.yml
	@# zizmor is installed separately (brew install zizmor); CI runs it too, online.
	@if command -v zizmor >/dev/null 2>&1; then zizmor --offline --no-progress .github; else echo "zizmor is not installed: skipped (CI runs it)"; fi

# Every go.mod asks for the oldest Go the project supports: the release before the
# current stable one (CI checks it, see .github/scripts/check_go_versions.py). When
# a release ships, the check goes red and this is the fix: make go-version GO=1.27
.PHONY: go-version
go-version:
	@case "$(GO)" in 1.[1-9]|1.[1-9][0-9]|1.[1-9][0-9][0-9]) ;; *) echo "GO is required, as a release such as 1.26" >&2; exit 1;; esac
	@set -e; for dir in $$(find . -name go.mod -not -path '*/testdata/*' -exec dirname {} \;); do \
		echo "go $(GO): $${dir}"; \
		(cd "$${dir}" && go mod edit -go=$(GO)); \
	done
