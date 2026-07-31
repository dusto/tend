# Releasing

Releases are built by [GoReleaser](https://goreleaser.com) and published as
GitHub Releases with prebuilt `tendd` + `tend` binaries for Linux and macOS
(amd64 + arm64). The version is stamped into `internal/version` at build time
(`tend --version`, and `tendd` logs it at startup).

There are three ways to cut a release; all end in the `release` workflow running
GoReleaser on a `v*` tag.

## 1. Merge a PR with a release label (the usual path)

Add one label to the PR before it merges:

| Label           | Bump  | Example        |
| --------------- | ----- | -------------- |
| `release:patch` | patch | v0.1.0 → v0.1.1 |
| `release:minor` | minor | v0.1.0 → v0.2.0 |
| `release:major` | major | v0.1.0 → v1.0.0 |

On merge, the `tag` workflow computes the next version from the latest `v*` tag,
pushes the new tag, and triggers `release`. A PR with no release label does not
release.

> One-time setup: create the labels in the repo, e.g.
> `gh label create release:patch release:minor release:major` (any color).

## 2. Trigger manually

Run the **tag** workflow from the Actions tab (or
`gh workflow run tag.yml -f bump=minor`) with a bump level. It tags and releases
the same way as the label path — useful for a release that isn't tied to a
single PR.

## 3. Push a tag yourself

A maintainer can push a tag directly and the `release` workflow builds it:

```sh
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

To re-run a release for an existing tag, dispatch the **release** workflow with
that tag selected as the ref.

## Validate the config

`goreleaser check` validates `.goreleaser.yaml`; CI runs it on every PR. To dry
run a build without publishing:

```sh
goreleaser release --snapshot --clean
```
