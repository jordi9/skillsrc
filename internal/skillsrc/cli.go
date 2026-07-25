package skillsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
)

func RunCLI(ctx context.Context, args []string, runtime CLIOptions) int {
	if runtime.Out == nil {
		runtime.Out = io.Discard
	}
	if runtime.Err == nil {
		runtime.Err = io.Discard
	}
	global := flag.NewFlagSet("skillsrc", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	var user, help bool
	global.BoolVar(&help, "h", false, "print help")
	global.BoolVar(&help, "help", false, "print help")
	global.BoolVar(&user, "g", false, "use the user-level configuration")
	global.BoolVar(&user, "global", false, "use the user-level configuration")
	global.BoolVar(&user, "user", false, "use the user-level configuration")
	manifest := global.String("manifest", "", "use an exact manifest path")
	lock := global.String("lock", "", "override the lockfile path")
	target := global.String("target", "", "override the installation target")
	cache := global.String("cache", runtime.CacheDir, "repository cache")
	git := global.String("git", runtime.GitBinary, "git executable")
	global.Usage = func() {}
	if err := global.Parse(args); err != nil {
		return cliUsageError(runtime.Err, err.Error())
	}
	if help {
		printUsage(runtime.Out)
		return 0
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		return cliUsageError(runtime.Err, "command required")
	}
	command := remaining[0]
	if command == "help" {
		printUsage(runtime.Out)
		return 0
	}
	known := map[string]bool{"init": true, "sync": true, "outdated": true, "update": true, "add": true, "remove": true, "rm": true, "list": true, "ls": true, "doctor": true}
	if !known[command] {
		return cliUsageError(runtime.Err, fmt.Sprintf("unknown command %q", command))
	}
	explicit := make(map[string]bool)
	global.Visit(func(found *flag.Flag) { explicit[found.Name] = true })
	request := ScopeRequest{
		User:             user,
		ManifestPath:     *manifest,
		ManifestExplicit: explicit["manifest"],
		LockPath:         *lock,
		LockExplicit:     explicit["lock"],
		TargetDir:        *target,
		TargetExplicit:   explicit["target"],
	}
	if request.User && request.ManifestExplicit {
		return cliUsageError(runtime.Err, "--global/--user cannot be combined with --manifest")
	}
	if command == "init" {
		if len(remaining) != 1 {
			return cliUsageError(runtime.Err, "init accepts no arguments")
		}
		layout, err := ResolveInitLayout(request, runtime)
		if err == nil {
			err = InitializeManifest(layout.ManifestPath)
		}
		if err == nil && layout.ProjectRoot != "" {
			err = EnsureRootGitignore(layout.ProjectRoot)
			if err == nil {
				err = WriteManagedGitignore(layout.ProjectRoot, Lock{Version: SchemaVersion})
			}
		}
		if err != nil {
			printCLIError(runtime.Err, err.Error(), runtime.HomeDir)
			return 1
		}
		fmt.Fprintf(runtime.Out, "  ✓ %s · initialized\n", displayPath(layout.ManifestPath, runtime.HomeDir))
		return 0
	}
	layout, err := ResolveLayout(request, runtime)
	if err != nil {
		printCLIError(runtime.Err, err.Error(), runtime.HomeDir)
		return 1
	}
	lockDir := runtime.LockDir
	if lockDir == "" {
		lockDir = filepath.Join(filepath.Dir(runtime.CacheDir), "locks")
	}
	options := Options{
		ProjectRoot:  layout.ProjectRoot,
		ManifestPath: layout.ManifestPath,
		LockPath:     layout.LockPath,
		TargetDir:    layout.TargetDir,
		CacheDir:     *cache,
		LockDir:      lockDir,
		GitBinary:    *git,
		Out:          runtime.Out,
		Err:          runtime.Err,
	}
	engine := NewEngine(options)

	switch command {
	case "sync":
		if len(remaining) != 1 {
			return cliUsageError(options.Err, "sync accepts no arguments")
		}
		var result Result
		result, err = engine.Sync(ctx)
		if err == nil {
			printResult(options.Out, "Sync complete", result, runtime.HomeDir, false)
		}
	case "outdated":
		var result OutdatedResult
		result, err = engine.Outdated(ctx, remaining[1:])
		if err == nil {
			printOutdated(options.Out, result, runtime.HomeDir)
		}
	case "update":
		var result Result
		result, err = engine.Update(ctx, remaining[1:])
		if err == nil {
			printFetches(options.Out, result.Fetches, runtime.HomeDir)
			for _, change := range result.Changes {
				old := displayCommit(change.Old)
				if old == "" {
					old = "(unlocked)"
				}
				fmt.Fprintf(options.Out, "  ✓ %s · %s → %s\n", displaySource(change.Source, runtime.HomeDir), old, displayCommit(change.New))
			}
			for _, local := range result.LocalSkipped {
				fmt.Fprintf(options.Out, "  • %s · local source, skipped\n", displaySource(local, runtime.HomeDir))
			}
			printResult(options.Out, "Update complete", result, runtime.HomeDir, true)
		}
	case "add":
		err = runAddCLI(ctx, engine, remaining[1:], options.Out, runtime.HomeDir)
	case "remove", "rm":
		err = runRemoveCLI(ctx, engine, remaining[1:], options.Out, runtime.HomeDir)
	case "list", "ls":
		err = runListCLI(ctx, engine, remaining[1:], options.Out, options.Err, runtime.HomeDir)
	case "doctor":
		var issues bool
		issues, err = runDoctorCLI(ctx, engine, remaining[1:], options.Out, options.Err, runtime.HomeDir)
		if err == nil && issues {
			return 1
		}
	}
	if err != nil {
		printCLIError(options.Err, err.Error(), runtime.HomeDir)
		return 1
	}
	return 0
}

func printCLIError(output io.Writer, message, home string) {
	message = strings.TrimPrefix(message, "validation: ")
	message = displayPathsInText(message, home)
	fmt.Fprintf(output, "Error: %s\n", styleCommandSuggestions(message, supportsColor(output)))
}

func styleCommandSuggestions(message string, color bool) string {
	if !color {
		return message
	}
	return strings.NewReplacer(
		"skillsrc init", "\x1b[1;36mskillsrc init\x1b[0m",
		"--global", "\x1b[1;36m--global\x1b[0m",
	).Replace(message)
}

func displayPath(path, home string) string {
	if home == "" {
		return path
	}
	relative, err := filepath.Rel(filepath.Clean(home), filepath.Clean(path))
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path
	}
	if relative == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + relative
}

func displayPossiblePath(value, home string) string {
	if filepath.IsAbs(value) {
		return displayPath(value, home)
	}
	return value
}

func displayPathsInText(text, home string) string {
	home = filepath.Clean(home)
	if home == "" || home == "." {
		return text
	}
	var result strings.Builder
	for {
		index := strings.Index(text, home)
		if index < 0 {
			result.WriteString(text)
			return result.String()
		}
		beforeBoundary := index == 0 || strings.ContainsRune(" \t\n\r\"'([{=:", rune(text[index-1]))
		after := index + len(home)
		afterBoundary := after == len(text) || text[after] == filepath.Separator
		if beforeBoundary && afterBoundary {
			result.WriteString(text[:index])
			result.WriteByte('~')
			text = text[after:]
			continue
		}
		result.WriteString(text[:after])
		text = text[after:]
	}
}

func printFetches(output io.Writer, fetches []FetchEvent, home string) {
	for _, fetch := range fetches {
		detail := humanFetchReason(fetch.Reason)
		if fetch.Commit != "" {
			commit := fetch.Commit
			if len(commit) > 12 {
				commit = commit[:12]
			}
			detail += " at " + commit
		}
		fmt.Fprintf(output, "  ↓ %s · fetched", displaySource(fetch.Source, home))
		if detail != "" {
			fmt.Fprintf(output, " · %s", detail)
		}
		fmt.Fprintln(output)
	}
}

func humanFetchReason(reason string) string {
	switch reason {
	case "new or changed declaration":
		return ""
	case "update configured ref":
		return ""
	case "locked commit missing from cache":
		return "restoring locked commit"
	case "exact configured commit missing from cache":
		return "restoring configured commit"
	default:
		return reason
	}
}

func displayCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func displayRevisionMetadata(revision GitRevision) string {
	if revision.Commit == "" {
		return "(unlocked)"
	}
	label := revision.Tag
	if label == "" {
		label = revision.Date
	}
	commit := displayCommit(revision.Commit)
	if label == "" || label == commit {
		return commit
	}
	return fmt.Sprintf("%s (%s)", label, commit)
}

func printOutdated(output io.Writer, result OutdatedResult, home string) {
	updates := 0
	for _, source := range result.Sources {
		name := displaySource(source.Source, home)
		if source.Old.Commit == source.New.Commit {
			fmt.Fprintf(output, "  ✓ %s · up to date\n", name)
			continue
		}
		updates++
		fmt.Fprintf(output, "  ↑ %s · update available · %s → %s\n", name, displayRevisionMetadata(source.Old), displayRevisionMetadata(source.New))
	}
	for _, local := range result.LocalSkipped {
		fmt.Fprintf(output, "  • %s · local source, skipped\n", displaySource(local, home))
	}
	if updates == 0 {
		if len(result.Sources) == 0 && len(result.LocalSkipped) == 0 {
			fmt.Fprintln(output, "  ✓ No Git sources to check")
		}
		return
	}
	fmt.Fprintln(output)
	noun := "updates"
	if updates == 1 {
		noun = "update"
	}
	fmt.Fprintf(output, "  └─ Summary · %d %s available\n", updates, noun)
}

func printResult(output io.Writer, label string, result Result, home string, fetchesPrinted bool) {
	if !fetchesPrinted {
		printFetches(output, result.Fetches, home)
	}
	counts := map[string]int{"installed": 0, "repaired": 0, "unchanged": 0, "pruned": 0}
	names := map[string][]string{}
	for _, skill := range result.Skills {
		counts[skill.Action]++
		if skill.Action != "unchanged" {
			names[skill.Action] = append(names[skill.Action], skill.Name)
		}
	}
	actionLabels := map[string]string{"installed": "installed", "repaired": "restored", "pruned": "removed"}
	printedDetails := len(result.Fetches) > 0
	for _, action := range []string{"installed", "repaired", "pruned"} {
		for _, name := range names[action] {
			fmt.Fprintf(output, "  ✓ %s · %s\n", name, actionLabels[action])
			printedDetails = true
		}
	}
	var summary []string
	for _, item := range []struct {
		count int
		label string
	}{
		{counts["installed"], "installed"},
		{counts["repaired"], "restored"},
		{counts["unchanged"], "up to date"},
		{counts["pruned"], "removed"},
	} {
		if item.count > 0 {
			summary = append(summary, fmt.Sprintf("%d %s", item.count, item.label))
		}
	}
	if fetched := len(result.Fetches); fetched > 0 {
		noun := "repositories"
		if fetched == 1 {
			noun = "repository"
		}
		summary = append(summary, fmt.Sprintf("%d %s fetched", fetched, noun))
	}
	if len(summary) == 0 {
		summary = append(summary, "no changes")
	}
	if printedDetails {
		fmt.Fprintln(output)
	}
	fmt.Fprintf(output, "  └─ %s · %s\n", label, strings.Join(summary, " · "))
}

func runAddCLI(ctx context.Context, engine *Engine, args []string, output io.Writer, home string) error {
	parsed, err := parseAddArgs(args)
	if err != nil {
		return err
	}
	source, err := addSource(engine.options.ManifestPath, parsed.source, parsed.ref)
	if err != nil {
		return err
	}
	if len(parsed.skills) == 0 && !parsed.all {
		names, result, err := engine.Discover(ctx, source)
		if err != nil {
			return err
		}
		printFetches(output, result.Fetches, home)
		if len(result.Fetches) > 0 {
			fmt.Fprintln(output)
		}
		fmt.Fprintf(output, "Available skills from %s:\n", displaySource(parsed.source, home))
		for _, name := range names {
			fmt.Fprintf(output, "  • %s\n", name)
		}
		return nil
	}
	_, result, err := engine.Add(ctx, source, parsed.skills, parsed.all, parsed.userOnly)
	if err != nil {
		return err
	}
	printResult(output, "Add complete", result, home, false)
	return nil
}

type addArguments struct {
	source   string
	skills   []string
	ref      string
	all      bool
	list     bool
	userOnly bool
}

var skillsCLISpecifierPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([A-Za-z0-9][A-Za-z0-9._-]*)$`)

func parseAddArgs(args []string) (addArguments, error) {
	var parsed addArguments
	var positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--all":
			parsed.all = true
		case argument == "--list":
			parsed.list = true
		case argument == "--invoke-user-only":
			parsed.userOnly = true
		case argument == "--ref":
			index++
			if index >= len(args) {
				return parsed, errors.New("--ref requires a branch, tag, or full commit hash")
			}
			parsed.ref = args[index]
		case strings.HasPrefix(argument, "--ref="):
			parsed.ref = strings.TrimPrefix(argument, "--ref=")
		case strings.HasPrefix(argument, "-"):
			return parsed, fmt.Errorf("unknown add option %q", argument)
		default:
			positional = append(positional, argument)
		}
	}
	if len(positional) == 0 {
		return parsed, errors.New("add requires a source; optionally followed by skill names")
	}
	parsed.source, parsed.skills = positional[0], positional[1:]
	if matches := skillsCLISpecifierPattern.FindStringSubmatch(parsed.source); matches != nil {
		if len(parsed.skills) > 0 {
			return parsed, errors.New("a source using @skill cannot be combined with positional skill names")
		}
		parsed.source, parsed.skills = matches[1], []string{matches[2]}
	}
	if parsed.all && len(parsed.skills) > 0 {
		return parsed, errors.New("--all cannot be combined with skill names")
	}
	if parsed.list && (parsed.all || len(parsed.skills) > 0) {
		return parsed, errors.New("--list cannot be combined with --all or skill names")
	}
	if parsed.userOnly && parsed.list {
		return parsed, errors.New("--invoke-user-only cannot be combined with --list")
	}
	if parsed.userOnly && !parsed.all && len(parsed.skills) == 0 {
		return parsed, errors.New("--invoke-user-only requires skill names or --all")
	}
	return parsed, nil
}

func runRemoveCLI(ctx context.Context, engine *Engine, args []string, output io.Writer, home string) error {
	if len(args) == 0 {
		return errors.New("remove requires at least one skill name")
	}
	result, err := engine.Remove(ctx, args)
	if err != nil {
		return err
	}
	printResult(output, "Remove complete", result, home, false)
	return nil
}

func addSource(manifestPath, input, ref string) (ManifestSource, error) {
	isLocal := filepath.IsAbs(input) || input == "." || input == ".." || input == "~" || strings.HasPrefix(input, "./") || strings.HasPrefix(input, "../") || strings.HasPrefix(input, "~/")
	if !isLocal {
		return ManifestSource{Repo: input, Ref: ref}, nil
	}
	if ref != "" {
		return ManifestSource{}, &ValidationError{Problem: "local source cannot set ref"}
	}
	resolved, err := resolveLocalPath(".", input)
	if err != nil {
		return ManifestSource{}, err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return ManifestSource{}, err
	}
	stored, err := filepath.Rel(filepath.Dir(manifestPath), absolute)
	if err != nil {
		stored = absolute
	}
	return ManifestSource{Path: filepath.ToSlash(stored), ResolvedPath: absolute}, nil
}

func runListCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer, home string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("list accepts no arguments")
	}
	statuses, err := engine.List(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(statuses)
	}
	if len(statuses) == 0 {
		_, err := fmt.Fprintln(output, "0 skills configured")
		return err
	}

	color := supportsColor(output)
	var table bytes.Buffer
	writer := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "SKILL\tSOURCE\tSTATUS\tREVISION")
	fmt.Fprintln(writer, "─────\t──────\t──────\t────────")
	counts := make(map[string]int)
	for _, status := range statuses {
		counts[status.Status]++
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", status.Name, displaySource(status.Source, home), displayState(status.Status, false), displayRevision(status))
	}
	if err := writer.Flush(); err != nil {
		return err
	}

	tableText := styleListHeader(table.String(), color)
	if color {
		tableText = styleListRows(tableText, statuses)
	}
	var result strings.Builder
	result.WriteString(tableText)
	result.WriteByte('\n')
	skillNoun := "skills"
	if len(statuses) == 1 {
		skillNoun = "skill"
	}
	fmt.Fprintf(&result, "%d %s", len(statuses), skillNoun)
	for _, state := range []string{"current", "missing", "drifted", "collision", "unlocked"} {
		if counts[state] == 0 {
			continue
		}
		_, label, _ := stateDisplay(state)
		fmt.Fprintf(&result, " · %d %s", counts[state], label)
	}
	result.WriteByte('\n')
	_, err = io.WriteString(output, result.String())
	return err
}

func supportsColor(output io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func styleListHeader(table string, color bool) string {
	if !color {
		return table
	}
	lines := strings.SplitAfter(table, "\n")
	if len(lines) >= 2 {
		lines[0] = "\x1b[1;36m" + strings.TrimSuffix(lines[0], "\n") + "\x1b[0m\n"
		lines[1] = "\x1b[2;36m" + strings.TrimSuffix(lines[1], "\n") + "\x1b[0m\n"
	}
	return strings.Join(lines, "")
}

func styleListRows(table string, statuses []SkillStatus) string {
	lines := strings.SplitAfter(table, "\n")
	for index, status := range statuses {
		lineIndex := index + 2
		if lineIndex >= len(lines) {
			break
		}
		line := strings.TrimSuffix(lines[lineIndex], "\n")
		plainState := displayState(status.Status, false)
		if stateIndex := strings.LastIndex(line, plainState); stateIndex >= 0 {
			line = line[:stateIndex] + displayState(status.Status, true) + line[stateIndex+len(plainState):]
		}
		revision := displayRevision(status)
		if strings.HasSuffix(line, revision) {
			line = strings.TrimSuffix(line, revision) + "\x1b[2m" + revision + "\x1b[0m"
		}
		lines[lineIndex] = line + "\n"
	}
	return strings.Join(lines, "")
}

func displaySource(source string, home ...string) string {
	for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/", "https://github.com/"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(source, prefix), ".git")
		}
	}
	if len(home) > 0 {
		return displayPossiblePath(source, home[0])
	}
	return source
}

func displayRevision(status SkillStatus) string {
	if status.LockedCommit == "" {
		if status.Status == "unlocked" && status.ConfiguredRef != "" {
			return status.ConfiguredRef
		}
		return "local"
	}
	commit := status.LockedCommit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if status.ConfiguredRef != "" {
		return status.ConfiguredRef + " @ " + commit
	}
	return commit
}

func displayState(state string, color bool) string {
	marker, label, colorCode := stateDisplay(state)
	text := marker + " " + label
	if !color {
		return text
	}
	return colorCode + text + "\x1b[0m"
}

func stateDisplay(state string) (marker, label, colorCode string) {
	switch state {
	case "current":
		return "✓", "synced", "\x1b[32m"
	case "drifted":
		return "!", "modified", "\x1b[33m"
	case "collision":
		return "!", "blocked", "\x1b[33m"
	case "missing":
		return "✗", "missing", "\x1b[31m"
	default:
		return "?", state, "\x1b[33m"
	}
}

func runDoctorCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer, home string) (bool, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repair := flags.Bool("repair", false, "repair by running sync")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		return false, errors.New("doctor accepts no arguments")
	}
	report, err := engine.Doctor(ctx, *repair)
	if err != nil {
		return false, err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			return false, err
		}
	} else if len(report.Issues) == 0 {
		fmt.Fprintln(output, "  ✓ No issues found")
	} else {
		for _, issue := range report.Issues {
			label := issue.Kind
			if issue.Skill != "" {
				label += "/" + issue.Skill
			}
			fmt.Fprintf(output, "  ! %s · %s\n", label, displayPathsInText(issue.Message, home))
		}
	}
	return len(report.Issues) > 0, nil
}

func cliUsageError(output io.Writer, message string) int {
	fmt.Fprintf(output, "Error: %s\n\n", message)
	fmt.Fprintln(output, "Usage: skillsrc [OPTIONS] <COMMAND>")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "For more information, run 'skillsrc --help'.")
	return 2
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, strings.TrimSpace(`skillsrc — Declarative skill dependencies for .agents/skills

Usage: skillsrc [OPTIONS] <COMMAND>

Commands:
  init      Initialize a manifest
  sync      Install the exact declared and locked skill set
  add       Add skills from a Git repository or local directory
  remove    Remove skills and their managed installations [alias: rm]
  outdated  Show available Git updates without changing project files
  update    Update Git revisions, then sync
  list      Show configured skills and installation state [alias: ls]
  doctor    Diagnose or repair lock, install, cache, and project metadata
  help      Print this message

Scope selection:
  By default, skillsrc uses the nearest project skills.toml.

  -g, --global       Use ~/.agents/skills.toml
      --user         Alias for --global
      --manifest PATH
                     Use the skills.toml at PATH instead of searching parent directories

Options:
      --lock PATH    Override the lockfile path
      --target PATH  Override the installation directory
      --cache PATH   Override the Git repository cache
      --git PATH     Override the Git executable
  -h, --help         Print help`))
}
