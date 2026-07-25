# Lockfile

`skills.lock` is generated beside the manifest by default. Project lockfiles are committed alongside `skills.toml`; do not add `skills.lock` to `.gitignore`. The lock is the reproducibility boundary for clones: `sync` uses its pinned Git commits, selected skill paths, and content hashes to reproduce an installation.

User-level (`--global`) and explicit-manifest configurations use the same lock format, but whether to commit those files is determined by their owner rather than by skillsrc.

The schema is version 1. A Git source records:

```toml
[[sources]]
kind = "git"
identity = "github.com/owner/repository"
repo = "owner/repository"
ref = "main"
commit = "0123456789abcdef0123456789abcdef01234567"

[[sources.skills]]
name = "one"
path = "skills/one"
hash = "sha256:..."
disable_model_invocation = true # only when disabled by the source
```

- `identity` is the normalized repository identity used for matching and caching.
- `repo` and optional `ref` preserve the manifest declaration.
- `commit` is the resolved exact Git commit.

A local source records no Git ref or commit:

```toml
[[sources]]
kind = "local"
identity = "local:../local-skills"
path = "../local-skills"

[[sources.skills]]
name = "private-skill"
path = "private-skill"
hash = "sha256:..."
```

For both kinds, each skill records its selected `name`, discovered source-relative `path` (`.` when the source root is the skill), and deterministic content `hash`. The optional `disable_model_invocation` flag records that the source skill already disables model invocation, allowing `list` to distinguish source behavior from a manifest override.

## Determinism

Lock output contains no timestamps. Sources are sorted by stable source fields, and skills within each source are sorted by name. Writing the same resolved inputs therefore produces the same bytes.

A relative local path is resolved from the manifest directory for reading, but its identity remains the cleaned relative declaration, such as `local:../local-skills`; it is not rewritten to a machine-specific absolute path. The lock's local `path` also preserves the manifest value. Absolute and `~/...` local declarations remain explicit identities rather than being converted into relative paths.
