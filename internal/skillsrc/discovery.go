package skillsrc

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const ownershipFile = ".skillsrc-managed.json"

type DiscoveredSkill struct {
	Name string
	Path string
}

func DiscoverSkills(root string) (map[string]DiscoveredSkill, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("read source root %q: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source root %q is not a real directory", root)
	}

	candidates := make([]string, 0)
	if isRegularFile(filepath.Join(root, "SKILL.md")) {
		candidates = append(candidates, root)
	}
	for _, parent := range []string{"", "skills", filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
		dir := filepath.Join(root, parent)
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, fmt.Errorf("scan %q: %w", dir, readErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(dir, entry.Name())
			if isRegularFile(filepath.Join(candidate, "SKILL.md")) {
				candidates = append(candidates, candidate)
				continue
			}
			// Some skill repositories group skills by one category below skills/*.
			if parent == "skills" {
				children, childErr := os.ReadDir(candidate)
				if childErr != nil {
					return nil, fmt.Errorf("scan skill category %q: %w", candidate, childErr)
				}
				for _, child := range children {
					childCandidate := filepath.Join(candidate, child.Name())
					if child.IsDir() && isRegularFile(filepath.Join(childCandidate, "SKILL.md")) {
						candidates = append(candidates, childCandidate)
					}
				}
			}
		}
	}

	found := make(map[string]DiscoveredSkill)
	for _, candidate := range candidates {
		if err := ensureNoSymlinkPath(root, candidate); err != nil {
			return nil, err
		}
		if _, err := HashSkill(candidate); err != nil {
			return nil, fmt.Errorf("validate discovered skill %q: %w", candidate, err)
		}
		name, err := skillName(candidate)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			return nil, fmt.Errorf("make skill path relative: %w", err)
		}
		if err := ValidateRelativeSkillPath(relative); err != nil && relative != "." {
			return nil, err
		}
		if relative == "." {
			relative = "."
		}
		if previous, exists := found[name]; exists && previous.Path != filepath.ToSlash(relative) {
			return nil, &ValidationError{Problem: fmt.Sprintf("ambiguous skill %q at %q and %q", name, previous.Path, filepath.ToSlash(relative))}
		}
		found[name] = DiscoveredSkill{Name: name, Path: filepath.ToSlash(relative)}
	}
	return found, nil
}

func ensureNoSymlinkPath(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &ValidationError{Problem: fmt.Sprintf("symlink path %q is not allowed", current)}
		}
	}
	return nil
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func skillName(dir string) (string, error) {
	file, err := os.Open(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("read SKILL.md in %q: %w", dir, err)
	}
	defer file.Close()

	name := ""
	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	if scanner.Scan() && strings.TrimSpace(scanner.Text()) == "---" {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "---" {
				break
			}
			if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "name" {
				name = strings.Trim(strings.TrimSpace(value), `"'`)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read SKILL.md in %q: %w", dir, err)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if !skillNamePattern.MatchString(name) || name == "." || name == ".." {
		return "", &ValidationError{Problem: fmt.Sprintf("invalid discovered skill name %q in %q", name, dir)}
	}
	return name, nil
}

func ValidateRelativeSkillPath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return &ValidationError{Problem: fmt.Sprintf("invalid skill path %q", path)}
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return &ValidationError{Problem: fmt.Sprintf("invalid or escaping skill path %q", path)}
	}
	return nil
}

func HashSkill(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("skill path %q is not a real directory", dir)
	}
	if !isRegularFile(filepath.Join(dir, "SKILL.md")) {
		return "", &ValidationError{Problem: fmt.Sprintf("skill %q has no regular SKILL.md", dir)}
	}

	files := make([]string, 0)
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if _, _, err := safeFilePath(dir, path); err != nil {
				return err
			}
		} else if !entry.Type().IsRegular() {
			return &ValidationError{Problem: fmt.Sprintf("non-regular file %q is not allowed", path)}
		}
		relative, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if relative != ownershipFile {
			files = append(files, relative)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("inspect skill %q: %w", dir, err)
	}
	sort.Strings(files)
	digest := sha256.New()
	for _, relative := range files {
		path := filepath.Join(dir, relative)
		resolved, info, err := safeFilePath(dir, path)
		if err != nil {
			return "", err
		}
		writeHashField(digest, filepath.ToSlash(relative))
		writeHashField(digest, strconv.FormatUint(uint64(info.Mode().Perm()), 8))
		file, err := os.Open(resolved)
		if err != nil {
			return "", fmt.Errorf("open %q: %w", path, err)
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("hash %q: %w", path, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close %q: %w", path, closeErr)
		}
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func safeFilePath(root, path string) (string, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("read %q: %w", path, err)
	}
	resolved := path
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", nil, fmt.Errorf("read symlink %q: %w", path, err)
		}
		if filepath.IsAbs(target) {
			return "", nil, &ValidationError{Problem: fmt.Sprintf("symlink %q has absolute target", path)}
		}
		resolved, err = filepath.EvalSymlinks(filepath.Join(filepath.Dir(path), target))
		if err != nil {
			return "", nil, &ValidationError{Problem: fmt.Sprintf("symlink %q cannot be resolved safely: %v", path, err)}
		}
		canonicalRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			return "", nil, fmt.Errorf("resolve skill root %q: %w", root, rootErr)
		}
		relative, err := filepath.Rel(canonicalRoot, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", nil, &ValidationError{Problem: fmt.Sprintf("symlink %q escapes skill root", path)}
		}
		info, err = os.Stat(resolved)
		if err != nil {
			return "", nil, fmt.Errorf("read symlink target %q: %w", resolved, err)
		}
	}
	if !info.Mode().IsRegular() {
		return "", nil, &ValidationError{Problem: fmt.Sprintf("path %q does not resolve to a regular file", path)}
	}
	return resolved, info, nil
}

func writeHashField(digest hash.Hash, value string) {
	_, _ = io.WriteString(digest, strconv.Itoa(len(value)))
	_, _ = io.WriteString(digest, ":")
	_, _ = io.WriteString(digest, value)
}
