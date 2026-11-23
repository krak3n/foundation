# Security policy

## Reporting a vulnerability

Please report security problems **privately**, not in a public issue or pull request.

Use GitHub's private vulnerability reporting: open the repository's **Security** tab and choose
**Report a vulnerability**. Include what you found, how to reproduce it, and which version or
commit it affects. A proof of concept helps.

I will acknowledge a report, keep you informed while it is looked at, and credit you in the
advisory if you want that. Please give me a reasonable chance to release a fix before you
disclose the problem publicly.

## Supported versions

This library is pre-1.0. Security fixes are made on the latest release only.

## Scope

In scope: the `go.krak3n.dev/foundation` module and its packages, and the build and release
workflows in this repository.

Out of scope: services built with the library (their configuration and deployment are their
own), and problems that need control of the process's environment, files or command line, which
the library treats as trusted (see "Security" in CONFIG.md for what that means for configuration).

## What the library does about security

- It has no third party dependencies.
- CI runs `govulncheck` on every change and weekly, and checks the workflows with `zizmor`.
- Configuration problems are logged with control characters in names and messages escaped, so a hostile key or variable name cannot forge log lines. Values are never logged. A handler you write still decides how the rest of a log line is written.
- Release workflows pin their actions to commit SHAs and never paste event data into scripts.

## Maintainers: settings that are not in the repository

These are repository settings, so they can't be checked in. Before the repository is made public:

- Enable **private vulnerability reporting**, Dependabot alerts and security updates, and secret
  scanning with push protection.
- Protect `main`: require pull requests and the CI checks, and block force pushes. The checks to
  require are the jobs of `ci.yml` (`test`, `tooling`, `go-version`, `staticcheck`) and of
  `security.yml` (`govulncheck`, `zizmor`). `test` runs for each Go version in its matrix, so each of
  those is a check to require.
- Add a tag ruleset limiting who can create `v*` and `telemetry/**/v*` tags.
- Set the default workflow token to read-only, and require approval for workflows from outside
  contributors (Settings, Actions, General).
- Consider CODEOWNERS for `.github/`, GitHub's immutable releases, and CodeQL (which needs the
  repository to be public, or GitHub Advanced Security).
