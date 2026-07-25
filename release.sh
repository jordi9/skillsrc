#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./release.sh [--dry-run]

Choose the next semantic version, preview its release notes, and push the tag.
GitHub Actions performs the release.
EOF
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

git_dir=$(jj git root)
git_command=(git --git-dir="$git_dir")

if ! "${git_command[@]}" rev-parse -q --verify refs/heads/main >/dev/null; then
  echo "Local main bookmark not found" >&2
  exit 1
fi

jj git fetch --remote origin
local_main=$("${git_command[@]}" rev-parse refs/heads/main)
remote_main=$("${git_command[@]}" rev-parse refs/remotes/origin/main)
if [[ "$local_main" != "$remote_main" ]]; then
  echo "Local main does not match origin/main; push or update main before releasing" >&2
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

echo
echo "Release notes preview for $version:"
echo
git-cliff --repository "$git_dir" --tag "$version" "$range"
echo

if [[ "$dry_run" == true ]]; then
  echo "[dry-run] Would tag main as $version and push the tag."
  exit 0
fi

read -r -p "Tag main as $version and publish the release? [y/N] " confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
  echo "Release cancelled."
  exit 0
fi

jj tag set "$version" -r main
"${git_command[@]}" push origin "refs/tags/$version"
echo "Pushed $version. GitHub Actions will publish the release."
