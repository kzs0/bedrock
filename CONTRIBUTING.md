# Contributing to Bedrock

Thanks for helping improve Bedrock. Small, focused changes with tests and clear
compatibility notes are easiest to review.

## Before starting

Search the [issue tracker](https://github.com/kzs0/bedrock/issues) for related
work. Open an issue before a large feature, public API change, or behavior
change so the scope can be agreed before implementation. Security reports must
follow [SECURITY.md](SECURITY.md), not the public issue tracker.

Bedrock requires Go 1.24 or newer. Clone your fork, create a topic branch, and
keep unrelated changes in separate pull requests.

## Make a change

- Preserve the context-based, dependency-light design and controlled metric
  cardinality.
- Add focused tests for success, failure, boundary, cancellation, and
  concurrency behavior where relevant.
- Prefer deterministic synchronization over sleeps. Run concurrency-sensitive
  tests with the race detector.
- Update public documentation and the `Unreleased` section of
  [CHANGELOG.md](CHANGELOG.md) when behavior, configuration, or API changes.
- Do not add or update dependencies unless the change requires it and the pull
  request explains the tradeoff.

Format and validate the repository before submitting:

```bash
go build ./...
make ci
```

Run `gofmt -w <changed-go-files>` and `go mod tidy` first; neither command
should leave unrelated changes. `make ci` checks formatting, vet, lint, ordinary
and race-enabled tests, 80% aggregate coverage, the nested `bench` module, and
known vulnerabilities in both modules. Run the aggregate target with Go 1.25,
which is required by the pinned vulnerability scanner; CI separately verifies
that the library builds and tests at its Go 1.24 baseline.

GitHub Actions builds and tests on Go 1.24 and 1.25. Separate Go 1.24 jobs run
the race detector, coverage gate, formatting, vet, lint, and `bench` module
tests; vulnerability scanning runs on Go 1.25 because the pinned scanner
requires it.

## Compatibility

The module is below v1.0. Minor releases can contain intentional API or behavior
changes, but changes should still minimize disruption. A breaking change needs:

- a clear reason and migration path;
- tests for the new contract;
- documentation updates; and
- an explicit changelog entry under `Changed`.

Bug fixes may reject input that was previously accepted incorrectly, preserve
data that was previously rounded or dropped, or make shutdown and network
behavior stricter. Call these effects out so operators can test their current
configuration before upgrading.

## Pull requests

Describe the problem, the chosen behavior, compatibility or security impact,
and the commands used to validate the change. Link related issues. Keep commits
reviewable; maintainers may squash or rebase when merging.

## Release process

Releases are maintainer-driven and GitHub Release publication is automated for
semantic-version tags. For a release, a maintainer:

1. moves relevant `Unreleased` entries to a dated `vX.Y.Z` section;
2. verifies the full CI-equivalent command set above on the release commit;
3. merges the release-ready change to `main`;
4. creates and pushes a `vX.Y.Z` tag, or a SemVer prerelease tag, on that commit;
5. confirms the tagged-release workflow verifies the source and creates the
   GitHub Release with generated notes; and
6. starts a fresh `Unreleased` section for subsequent work.

Release tags must point to `main`. Tags containing a prerelease suffix create a
prerelease. Do not retag an existing version; publish a new patch version for
corrections.
