# Cut a release

Run the checks on `main` before tagging. Pick a tag such as `v0.1.0` for a release or `v0.1.0-rc.1` for a release candidate.

Pushing a `v*` tag starts the release workflow. Publication requires a pushed tag that matches `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`.

```sh
git switch main
git pull --ff-only origin main
make check
make build
tag=v0.1.0
git tag -a "$tag" -m "OpenPlexer $tag"
git push origin "refs/tags/$tag"
```

Open the [release workflow](https://github.com/Multiplexer-si/OpenPlexer/actions/workflows/release.yml) and check that the run for the tag passed. On the [releases page](https://github.com/Multiplexer-si/OpenPlexer/releases), check that the release has five platform archives and `SHA256SUMS`. Tags containing a hyphen are prereleases. Download your platform's archive and follow the [install guide](install.md).

The Linux, macOS, and Windows binaries are built with CGO disabled.

To build the archives on demand, choose **Run workflow** on the release workflow page. Download `openplexer-release` from the completed run's artifacts. A run on a branch uses `v0.0.0-dev.` followed by the short commit ID as its version.

## Rebuild and compare

Use a clean Git checkout with LF line endings and the exact Go version shown in the original workflow's setup log. Set `tag` to the release you want to check. The release tool uses the checked-out commit's timestamp unless you set `SOURCE_DATE_EPOCH` to a Unix timestamp in seconds. Leave it unset to match the workflow.

The build requires a new output directory. By default it publishes the complete set of files to `dist/release/<tag>/` when the build succeeds. Use `-out` with a new directory to rebuild the same tag again.

```sh
tag=v0.1.0
git fetch origin "refs/tags/$tag:refs/tags/$tag"
git switch --detach "$tag"
unset SOURCE_DATE_EPOCH
make release VERSION="$tag"
gh release download "$tag" --repo Multiplexer-si/OpenPlexer --pattern SHA256SUMS --output dist/published-SHA256SUMS
cmp dist/published-SHA256SUMS "dist/release/$tag/SHA256SUMS"
```

Sign in to the GitHub CLI before running the download command. If the checksum lists match, `cmp` prints nothing and exits with status 0. To rebuild one archive, pass its target to the release tool and check the matching line from the published manifest. This example checks Apple silicon.

```sh
go run ./tools/release -version "$tag" -target darwin/arm64 -out dist/rebuild
(
	cd dist/rebuild
	awk -v name="openplexer_${tag}_darwin_arm64.tar.gz" '$2 == name' ../published-SHA256SUMS | shasum -a 256 -c
)
```
