package skillsrc

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	priority    int
	marketplace bool
	hash        string
}

type skillCandidatePath struct {
	path        string
	priority    int
	marketplace bool
}

func DiscoverSkills(root string) (map[string]DiscoveredSkill, error) {
	discovered, _, err := DiscoverSkillsWithWarnings(root)
	return discovered, err
}

func DiscoverSkillsWithWarnings(root string) (map[string]DiscoveredSkill, []string, error) {
	discovered, err := discoverSkillCandidates(root)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string]DiscoveredSkill)
	selected := make(map[string]discoveredSkillCandidate)
	var warnings []string
	for _, skill := range discovered {
		previous, exists := selected[skill.Name]
		if !exists || skill.priority < previous.priority {
			selected[skill.Name] = skill
			result[skill.Name] = skill.DiscoveredSkill
			continue
		}
		if skill.priority > previous.priority || previous.Path == skill.Path || previous.hash == skill.hash {
			continue
		}
		if !previous.marketplace || !skill.marketplace {
			return nil, nil, ambiguousSkill(skill.Name, previous.Path, skill.Path)
		}
		warnings = append(warnings, fmt.Sprintf("duplicate skill %q; using %q and ignoring %q", skill.Name, previous.Path, skill.Path))
	}
	return result, warnings, nil
}

func ambiguousSkill(name, first, second string) error {
	return &ValidationError{Problem: fmt.Sprintf("ambiguous skill %q at %q and %q", name, first, second)}
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
			if parent == "skills" {
				categorized, err := collectCategorizedSkillPaths(candidate, priority)
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, categorized...)
			}
		}
	}
	claude, err := collectClaudePluginSkillPaths(root, 5)
	if err != nil {
		return nil, err
	}
	cursor, err := collectCursorPluginSkillPaths(root, 5)
	if err != nil {
		return nil, err
	}
	return append(candidates, append(claude, cursor...)...), nil
}

const claudePluginManifestDir = ".claude-plugin"

type marketplaceManifest struct {
	Metadata struct {
		PluginRoot string `json:"pluginRoot"`
	} `json:"metadata"`
	Plugins []marketplacePlugin `json:"plugins"`
}

type marketplacePlugin struct {
	Source json.RawMessage `json:"source"`
	Skills []string        `json:"skills"`
}

type claudePluginManifest struct {
	Skills []string `json:"skills"`
}

type cursorPluginManifest struct {
	Skills json.RawMessage `json:"skills"`
}

func collectClaudePluginSkillPaths(root string, priority int) ([]skillCandidatePath, error) {
	var candidates []skillCandidatePath
	var marketplace marketplaceManifest
	if readJSONFile(filepath.Join(root, claudePluginManifestDir, "marketplace.json"), &marketplace) {
		pluginRoot := root
		if marketplace.Metadata.PluginRoot != "" {
			var ok bool
			pluginRoot, ok = safeManifestPath(root, root, marketplace.Metadata.PluginRoot, false)
			if !ok {
				pluginRoot = ""
			}
		}
		for _, plugin := range marketplace.Plugins {
			if pluginRoot == "" {
				break
			}
			pluginBase := pluginRoot
			if len(plugin.Source) != 0 {
				var source string
				if json.Unmarshal(plugin.Source, &source) != nil {
					continue
				}
				var ok bool
				pluginBase, ok = safeManifestPath(root, pluginRoot, source, false)
				if !ok {
					continue
				}
			}
			paths, err := collectClaudeManifestSkills(root, pluginBase, plugin.Skills, priority, true)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, paths...)
		}
	}

	var plugin claudePluginManifest
	if readJSONFile(filepath.Join(root, claudePluginManifestDir, "plugin.json"), &plugin) {
		paths, err := collectClaudeManifestSkills(root, root, plugin.Skills, priority, false)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, paths...)
	}
	return candidates, nil
}

func collectClaudeManifestSkills(root, pluginBase string, declared []string, priority int, marketplace bool) ([]skillCandidatePath, error) {
	var candidates []skillCandidatePath
	for _, skill := range declared {
		path, ok := safeManifestPath(root, pluginBase, skill, false)
		if ok && isRegularFile(filepath.Join(path, "SKILL.md")) {
			candidates = appendCandidatePath(candidates, skillCandidatePath{path: path, priority: priority, marketplace: marketplace})
		}
	}
	paths, err := collectSkillDirectory(filepath.Join(pluginBase, "skills"), priority, marketplace)
	if err != nil {
		return nil, fmt.Errorf("scan Claude plugin skills in %q: %w", pluginBase, err)
	}
	for _, path := range paths {
		candidates = appendCandidatePath(candidates, path)
	}
	return candidates, nil
}

func collectCursorPluginSkillPaths(root string, priority int) ([]skillCandidatePath, error) {
	var candidates []skillCandidatePath
	var marketplace marketplaceManifest
	if readJSONFile(filepath.Join(root, ".cursor-plugin", "marketplace.json"), &marketplace) {
		for _, listed := range marketplace.Plugins {
			var source string
			if json.Unmarshal(listed.Source, &source) != nil {
				continue
			}
			pluginBase, ok := safeManifestPath(root, root, source, true)
			if !ok {
				continue
			}
			var manifest cursorPluginManifest
			if !readJSONFile(filepath.Join(pluginBase, ".cursor-plugin", "plugin.json"), &manifest) {
				continue
			}
			paths, err := collectCursorManifestSkills(root, pluginBase, manifest, priority)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, paths...)
		}
	}

	var rootPlugin cursorPluginManifest
	if readJSONFile(filepath.Join(root, ".cursor-plugin", "plugin.json"), &rootPlugin) {
		paths, err := collectCursorManifestSkills(root, root, rootPlugin, priority)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			candidates = appendCandidatePath(candidates, path)
		}
	}
	return candidates, nil
}

func collectCursorManifestSkills(root, pluginBase string, manifest cursorPluginManifest, priority int) ([]skillCandidatePath, error) {
	declared, valid := parseManifestStringList(manifest.Skills)
	if !valid {
		return nil, nil
	}
	if len(manifest.Skills) == 0 {
		declared = []string{"skills"}
	}
	var candidates []skillCandidatePath
	for _, directory := range declared {
		dir, ok := safeManifestPath(root, pluginBase, directory, true)
		if !ok {
			continue
		}
		paths, err := collectSkillDirectory(dir, priority, true)
		if err != nil {
			return nil, fmt.Errorf("scan Cursor plugin skills in %q: %w", pluginBase, err)
		}
		candidates = append(candidates, paths...)
	}
	return candidates, nil
}

func parseManifestStringList(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, true
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many, true
	}
	return nil, false
}

func collectSkillDirectory(dir string, priority int, marketplace bool) ([]skillCandidatePath, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var candidates []skillCandidatePath
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() && isRegularFile(filepath.Join(path, "SKILL.md")) {
			candidates = append(candidates, skillCandidatePath{path: path, priority: priority, marketplace: marketplace})
		}
	}
	return candidates, nil
}

func appendCandidatePath(candidates []skillCandidatePath, addition skillCandidatePath) []skillCandidatePath {
	for _, candidate := range candidates {
		if filepath.Clean(candidate.path) == filepath.Clean(addition.path) {
			return candidates
		}
	}
	return append(candidates, addition)
}

func safeManifestPath(root, base, path string, allowBare bool) (string, bool) {
	if path == "" || (!allowBare && !strings.HasPrefix(path, "./")) || strings.Contains(path, ":") {
		return "", false
	}
	path = strings.TrimPrefix(path, "./")
	relative := filepath.FromSlash(path)
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) || !filepath.IsLocal(relative) {
		return "", false
	}
	candidate := filepath.Clean(filepath.Join(base, relative))
	withinRoot, err := filepath.Rel(root, candidate)
	if err != nil || withinRoot == ".." || strings.HasPrefix(withinRoot, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

func readJSONFile(path string, target any) bool {
	content, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(content, target) != nil {
		return false
	}
	return true
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
		hash, err := HashSkill(candidate.path)
		if err != nil {
			return nil, fmt.Errorf("validate discovered skill %q: %w", candidate.path, err)
		}
		name, err := skillName(candidate.path)
		if err != nil && candidate.marketplace {
			var validation *ValidationError
			fallback := filepath.Base(candidate.path)
			if errors.As(err, &validation) && strings.Contains(validation.Problem, "invalid discovered skill name") && skillNamePattern.MatchString(fallback) && fallback != "." && fallback != ".." {
				name, err = fallback, nil
			}
		}
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
			marketplace:     candidate.marketplace,
			hash:            hash,
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
