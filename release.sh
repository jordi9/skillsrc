#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./release.sh [--dry-run]

Choose the next semantic version, generate and edit its changelog entry, then
push the release commit and tag. GitHub Actions performs the release.
EOF
}

extract_release_notes() {
  local version=$1
  local changelog=$2

  awk -v heading="## [$version]" '
    index($0, heading) == 1 {
      found = 1
      next
    }
    found && /^## \[/ {
      exit
    }
    found {
      print
      if ($0 !~ /^[[:space:]]*$/) {
        content = 1
      }
    }
    END {
      if (!found || !content) {
        exit 1
      }
    }
  ' "$changelog"
}

dry_run=false
case "${1:-}" in
  --dry-run) dry_run=true; shift ;;
  -h|--help) usage; exit 0 ;;
esac
if [[ $# -ne 0 ]]; then
  usage >&2
  exit 2
fi

for command in git git-cliff jj; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Required command not found: $command" >&2
    exit 1
  fi
done

if ! jj diff --quiet; then
  echo "The working copy has changes; commit or discard them before releasing" >&2
  exit 1
fi

git_dir=$(jj git root)
git_command=(git --git-dir="$git_dir")

if ! "${git_command[@]}" rev-parse -q --verify refs/heads/main >/dev/null; then
  echo "Local main bookmark not found" >&2
  exit 1
fi

local_main=$("${git_command[@]}" rev-parse refs/heads/main)
if [[ "$dry_run" == true ]]; then
  remote_main=$("${git_command[@]}" ls-remote --exit-code origin refs/heads/main | awk '{ print $1 }')
else
  jj git fetch --remote origin
  remote_main=$("${git_command[@]}" rev-parse refs/remotes/origin/main)
fi
parent_commit=$(jj log -r '@-' --no-graph -T 'commit_id')
if [[ "$local_main" != "$remote_main" ]]; then
  echo "Local main does not match origin/main; push or update main before releasing" >&2
  exit 1
fi
if [[ "$parent_commit" != "$local_main" ]]; then
  echo "The empty working-copy change must be directly on main before releasing" >&2
  exit 1
fi

latest=""
while IFS= read -r tag; do
  if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    latest=$tag
    break
  fi
done < <("${git_command[@]}" tag --list 'v*' --sort=-v:refname)

if [[ -z "$latest" ]]; then
  major=0
  minor=0
  patch=0
  range=$local_main
  current="none"
else
  current=$latest
  version=${latest#v}
  IFS=. read -r major minor patch <<< "$version"
  range="$latest..$local_main"

  if [[ $("${git_command[@]}" rev-list --count "$range") -eq 0 ]]; then
    echo "No commits on main since $latest; move and push main before releasing" >&2
    exit 1
  fi
fi

patch_version="v${major}.${minor}.$((patch + 1))"
minor_version="v${major}.$((minor + 1)).0"
major_version="v$((major + 1)).0.0"

echo "Current version: $current"
echo
echo "  1) Patch  -> $patch_version"
echo "  2) Minor  -> $minor_version"
echo "  3) Major  -> $major_version"
echo
read -r -p "Select next version [1-3]: " choice
case "$choice" in
  1|patch) version=$patch_version ;;
  2|minor) version=$minor_version ;;
  3|major) version=$major_version ;;
  *) echo "Invalid selection: $choice" >&2; exit 2 ;;
esac

if "${git_command[@]}" rev-parse -q --verify "refs/tags/$version" >/dev/null; then
  echo "Tag already exists: $version" >&2
  exit 1
fi

if [[ "$dry_run" == true ]]; then
  echo
  echo "Release notes preview for $version:"
  echo
  git-cliff --repository "$git_dir" --unreleased --tag "$version"
  echo
  echo "[dry-run] Would update CHANGELOG.md, open it in an editor, commit it, and push $version."
  exit 0
fi

git-cliff --repository "$git_dir" --unreleased --tag "$version" --prepend CHANGELOG.md

editor=${VISUAL:-${EDITOR:-vi}}
read -r -a editor_command <<< "$editor"
"${editor_command[@]}" CHANGELOG.md

notes_file=$(mktemp)
trap 'rm -f "$notes_file"' EXIT
if ! extract_release_notes "$version" CHANGELOG.md > "$notes_file"; then
  echo "CHANGELOG.md must contain a non-empty section headed ## [$version]" >&2
  exit 1
fi

echo
echo "Release notes for $version:"
echo
cat "$notes_file"
echo
read -r -p "Commit these notes and publish $version? [y/N] " confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
  echo "Release cancelled. Your CHANGELOG.md edits have been kept."
  exit 0
fi

jj describe -m "chore: prepare $version release"
release_commit=$(jj log -r @ --no-graph -T 'commit_id')
if ! "${git_command[@]}" push --atomic origin \
  "$release_commit:refs/heads/main" \
  "$release_commit:refs/tags/$version"; then
  echo "Atomic push failed; origin/main and $version were left unchanged." >&2
  echo "Your release commit and CHANGELOG.md edits remain in the working copy." >&2
  exit 1
fi
jj bookmark set main -r @
jj tag set "$version" -r main
jj new main

echo "Pushed $version. GitHub Actions will publish the committed changelog entry."
