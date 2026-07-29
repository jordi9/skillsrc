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

type discoveredSkillCandidate struct {
	DiscoveredSkill
	priority int
}

type skillCandidatePath struct {
	path     string
	priority int
}

func DiscoverSkills(root string) (map[string]DiscoveredSkill, error) {
	discovered, err := discoverSkillCandidates(root)
	if err != nil {
		return nil, err
	}
	found := make(map[string]DiscoveredSkill, len(discovered))
	priorities := make(map[string]int, len(discovered))
	for _, skill := range discovered {
		if previous, exists := found[skill.Name]; exists {
			previousPriority := priorities[skill.Name]
			if previousPriority == skill.priority {
				return nil, &ValidationError{Problem: fmt.Sprintf("ambiguous skill %q at %q and %q", skill.Name, previous.Path, skill.Path)}
			}
			if skill.priority < previousPriority {
				found[skill.Name] = skill.DiscoveredSkill
				priorities[skill.Name] = skill.priority
			}
			continue
		}
		found[skill.Name] = skill.DiscoveredSkill
		priorities[skill.Name] = skill.priority
	}
	return found, nil
}

func discoverSkillCandidates(root string) ([]discoveredSkillCandidate, error) {
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

	candidatePaths, err := collectSkillCandidatePaths(root)
	if err != nil {
		return nil, err
	}
	return materializeSkillCandidates(root, candidatePaths)
}

func collectSkillCandidatePaths(root string) ([]skillCandidatePath, error) {
	candidates := make([]skillCandidatePath, 0)
	if isRegularFile(filepath.Join(root, "SKILL.md")) {
		candidates = append(candidates, skillCandidatePath{path: root, priority: 1})
	}
	for index, parent := range []string{"", "skills", filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")} {
		priority := index + 1
		dir := filepath.Join(root, parent)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan %q: %w", dir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(dir, entry.Name())
			if isRegularFile(filepath.Join(candidate, "SKILL.md")) {
				candidates = append(candidates, skillCandidatePath{path: candidate, priority: priority})
				continue
			}
			// Some skill repositories group skills by one category below skills/*.
			if parent == "skills" {
				categorized, err := collectCategorizedSkillPaths(candidate, priority)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, categorized...)
			}
		}
	}
	return candidates, nil
}

func collectCategorizedSkillPaths(category string, priority int) ([]skillCandidatePath, error) {
	children, err := os.ReadDir(category)
	if err != nil {
		return nil, fmt.Errorf("scan skill category %q: %w", category, err)
	}
	var candidates []skillCandidatePath
	for _, child := range children {
		candidate := filepath.Join(category, child.Name())
		if child.IsDir() && isRegularFile(filepath.Join(candidate, "SKILL.md")) {
			candidates = append(candidates, skillCandidatePath{path: candidate, priority: priority})
		}
	}
	return candidates, nil
}

func materializeSkillCandidates(root string, candidates []skillCandidatePath) ([]discoveredSkillCandidate, error) {
	discovered := make([]discoveredSkillCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ensureNoSymlinkPath(root, candidate.path); err != nil {
			return nil, err
		}
		if _, err := HashSkill(candidate.path); err != nil {
			return nil, fmt.Errorf("validate discovered skill %q: %w", candidate.path, err)
		}
		name, err := skillName(candidate.path)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, candidate.path)
		if err != nil {
			return nil, fmt.Errorf("make skill path relative: %w", err)
		}
		if err := ValidateRelativeSkillPath(relative); err != nil && relative != "." {
			return nil, err
		}
		discovered = append(discovered, discoveredSkillCandidate{
			DiscoveredSkill: DiscoveredSkill{Name: name, Path: filepath.ToSlash(relative)},
			priority:        candidate.priority,
		})
	}
	return discovered, nil
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
	lines, err := skillFrontmatterLines(dir)
	if err != nil {
		return "", err
	}
	name := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "name" {
			name = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if !skillNamePattern.MatchString(name) || name == "." || name == ".." {
		return "", &ValidationError{Problem: fmt.Sprintf("invalid discovered skill name %q in %q", name, dir)}
	}
	return name, nil
}

func sourceDisablesModelInvocation(dir string) (bool, error) {
	lines, err := skillFrontmatterLines(dir)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "disable-model-invocation" {
			continue
		}
		value, _, _ = strings.Cut(value, "#")
		return strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"'`), "true"), nil
	}
	return false, nil
}

func skillFrontmatterLines(dir string) ([]string, error) {
	file, err := os.Open(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md in %q: %w", dir, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil, scanner.Err()
	}
	var lines []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "---" {
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SKILL.md in %q: %w", dir, err)
	}
	return lines, nil
}

func ValidateRelativeSkillPath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return &ValidationError{Problem: fmt.Sprintf("invalid skill path %q", path)}
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || !filepath.IsLocal(clean) {
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

	files, err := collectSkillFiles(dir)
	if err != nil {
		return "", fmt.Errorf("inspect skill %q: %w", dir, err)
	}
	return hashSkillFiles(dir, files)
}

func collectSkillFiles(dir string) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
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
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func hashSkillFiles(dir string, files []string) (string, error) {
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
