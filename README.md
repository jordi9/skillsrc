# skillsrc

**Declarative skill dependencies for `.agents/skills`.** `skillsrc` declares, locks, and safely installs Agent Skills from Git repositories or local directories.

## Defaults

| Item | Default |
| --- | --- |
| Manifest | `~/.agents/skills.toml` |
| Lockfile | `skills.lock` beside the manifest (`~/.agents/skills.lock`) |
| Installed skills | `~/.agents/skills` |
| Git cache | the OS user cache directory plus `skillsrc/repos` (for example, `~/Library/Caches/skillsrc/repos` on macOS) |

Override these with `--manifest`, `--lock`, `--target`, and `--cache` before the command.

## Manifest

The TOML schema is version 1. Each source selects one or more unique skill names and declares exactly one of `repo` or `path`.

```toml
version = 1

[[sources]]
repo = "owner/repository" # also accepts HTTPS and SSH Git URLs
ref = "main"              # optional branch, tag, or full 40-character commit hash
skills = ["one", "two"]

[[sources]]
path = "../local-skills"  # resolved relative to this manifest; ~/... is supported
skills = ["private-skill"]
```

A local source cannot set `ref`. Selected skills must be discoverable in the source and cannot be selected by more than one source.

Discovery is bounded to a repository root containing `SKILL.md`, immediate child directories, `skills/*` (plus one category level used by grouped repositories), `.agents/skills/*`, and `.claude/skills/*`. Duplicate discovered names are rejected. Safe relative file symlinks that resolve inside a selected skill are copied as regular files; absolute, escaping, directory, broken, and cyclic symlinks are rejected.

## Commands

```sh
skillsrc add SOURCE                         # list discovered skills
skillsrc add SOURCE SKILL...                # add selected skills and sync
skillsrc add SOURCE --all                   # add every discovered skill and sync
skillsrc add SOURCE SKILL... --ref REF      # branch, tag, or full commit hash
skillsrc remove SKILL...                    # remove declarations and sync
skillsrc sync
skillsrc update [source-or-skill ...]
skillsrc list [--json]
skillsrc doctor [--repair] [--json]
```

`add` accepts GitHub shorthand, HTTPS or SSH Git URLs, and local paths beginning with `./`, `../`, `/`, or `~`. With no skill names, it only lists what the source contains. `--list` makes that intent explicit. Added skills are validated before the manifest changes; `--all` cannot be combined with names.

`remove` deletes the named declarations, removes empty source blocks, and prunes only installations owned by this manifest. It does not update the remaining Git sources.

`sync` reproduces locked Git commits, creates missing lock entries, refreshes local content, installs the declared set, and prunes no-longer-declared managed skills. It does not advance an existing Git lock.

`update` refreshes all Git sources, or only sources matched by the supplied repository/path or skill selectors, then performs a sync. Local sources have no remote version; full commit refs remain exact.

See [docs/lockfile.md](docs/lockfile.md) for the generated lock format.

## Safety and recovery

Each installed skill contains `.skillsrc-managed.json`, tied to the manifest that owns it. `skillsrc` replaces or removes only directories carrying a valid marker for that manifest. A file, symlink, unmarked directory, or directory owned by another manifest is an unmanaged collision: the operation stops without overwriting it.

Installs use staging directories, backups, and transaction journals. The next mutating operation automatically recovers an interrupted replacement or prune. Use `skillsrc doctor` to report lock, install, and cache problems; `skillsrc doctor --repair` first runs `sync` to restore repairable state. Unmanaged collisions must be resolved by the user.

## Install or build

A working `git` executable is the only runtime dependency.

```sh
go install github.com/jordi9/skillsrc/cmd/skillsrc@latest
```

From a checkout, with `GOBIN` configured:

```sh
make install
```

Release packaging is described in [docs/release-plan.md](docs/release-plan.md).
