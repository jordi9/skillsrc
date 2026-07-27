# Release plan

Releases are tag-driven:

1. `release.sh` finds the latest release, asks for a patch, minor, or major bump, and pushes the calculated tag from
   `main`.
2. GitHub Actions validates the tag, runs tests and vet, and verifies a release build.
3. `git-cliff` generates release notes from the repository history.
4. GoReleaser builds macOS and Linux archives for arm64 and amd64, generates `checksums.txt`, and publishes the GitHub
   Release.

`.goreleaser.yaml` is the source of truth for release targets, build flags, archive contents, and asset names.

## Release

Move and push the `main` bookmark before tagging, then preview or publish:

```sh
./release.sh --dry-run
./release.sh
```

The script refuses to release unless local `main` matches `origin/main`. It calculates the three semantic-version
candidates from the latest stable tag, previews release notes for the exact `main` commit, and asks for confirmation.
Pushing the selected tag is the only release trigger.

Each release contains:

```text
skillsrc_<version>_darwin_arm64.tar.gz
skillsrc_<version>_darwin_amd64.tar.gz
skillsrc_<version>_linux_arm64.tar.gz
skillsrc_<version>_linux_amd64.tar.gz
checksums.txt
```

The tag is injected into each binary and verified with `skillsrc version` before publication. A public semantic version
tag also makes the command available through:

```sh
go install github.com/jordi9/skillsrc/cmd/skillsrc@<version>
```

## Homebrew

After the archive names have settled, create a `jordi9/homebrew-tap` repository with a `skillsrc` formula that downloads
the matching archive and verifies its checksum. It will expose:

```sh
brew install jordi9/tap/skillsrc
```

Automating formula updates can be added after the first manual formula release.
