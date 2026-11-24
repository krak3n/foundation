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
- The release pull requests are made with a short lived token from a GitHub App (so that they run
  CI), whose key is used by no other workflow: see "The release App" below.

## Maintainers: settings that are not in the repository

These are repository settings, so they can't be checked in. Before the repository is made public:

- Enable **private vulnerability reporting**, Dependabot alerts and security updates, and secret
  scanning with push protection.
- Protect `main`: require pull requests and the CI checks, and block force pushes. The checks to
  require are the jobs of `ci.yml` (`test`, `tooling`, `go-version`, `staticcheck`, `commits`) and of
  `security.yml` (`govulncheck`, `zizmor`). `test` runs for each Go version in its matrix, so each of
  those is a check to require.
- Add a tag ruleset for **all tags** (not patterns, so a module added later is covered, and a
  `*` that doesn't match `/` can't leave `config/yaml/v*` out) that restricts creation, updates and
  deletion and blocks force pushes. Let the repository admin role and the release App bypass it:
  release-please makes the tags with the App's token.
- Set the default workflow token to read-only, and require approval for workflows from outside
  contributors (Settings, Actions, General).
- **The release App.** Make a GitHub App that can be installed only on your account, with no webhook
  and only Contents and Pull requests (read and write) and Metadata (read), and install it on this
  repository alone. Keep its ID in the variable `RELEASE_APP_ID` and its private key in the secret
  `RELEASE_APP_PRIVATE_KEY` of an environment named `Release` (not a repository secret), and
  delete the downloaded key file. The key can write to the repository, so it must be rotated if it
  may have leaked, and the App must not be given administration, workflows or secrets access:
  `check_workflows.py` refuses a workflow that uses the key anywhere but the token step, in a
  workflow a pull request can start, or in a job outside `Release`, or that asks for more than
  those two permissions or more than this repository.
- Consider CODEOWNERS for `.github/`, GitHub's immutable releases, and CodeQL (which needs the
  repository to be public, or GitHub Advanced Security).
