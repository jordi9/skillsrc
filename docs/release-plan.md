# Release plan

There is no release automation yet. Keep the first release process manual and small.

For each tagged GitHub Release:

1. Build `skillsrc` for the supported matrix:
   - `darwin/arm64`
   - `darwin/amd64`
   - `linux/arm64`
   - `linux/amd64`
2. Package one archive per target, named consistently (for example, `skillsrc_<version>_<os>_<arch>.tar.gz`).
3. Publish the four archives and a SHA-256 checksum file on the GitHub Release.
4. Smoke-test the binaries with `skillsrc help` on available target systems.

Eventually, add a `skillsrc` formula to [`jordi9/tap`](https://github.com/jordi9/homebrew-tap). The formula should download the matching GitHub Release archive, verify its checksum, install the binary, and expose installation as:

```sh
brew install jordi9/tap/skillsrc
```

Do not add release workflows, cross-build scripts, or automatic Homebrew updates until the manual artifact names and release process have settled.
