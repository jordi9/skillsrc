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
	invocation, exitCode, done := prepareCLIInvocation(args, runtime)
	if done {
		return exitCode
	}
	return runCLIInvocation(ctx, invocation, runtime)
}

type cliInvocation struct {
	command, cacheDir, gitBinary string
	args                         []string
	request                      ScopeRequest
	autoUserScope                bool
}

func prepareCLIInvocation(args []string, runtime CLIOptions) (cliInvocation, int, bool) {
	global := flag.NewFlagSet("skillsrc", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	var user, help, showVersion bool
	global.BoolVar(&help, "h", false, "print help")
	global.BoolVar(&help, "help", false, "print help")
	global.BoolVar(&showVersion, "version", false, "print version")
	global.BoolVar(&user, "g", false, "use the user-level configuration")
	global.BoolVar(&user, "global", false, "use the user-level configuration")
	global.BoolVar(&user, "user", false, "use the user-level configuration")
	manifest := global.String("manifest", "", "use an exact manifest path")
	lock := global.String("lock", "", "override the lockfile path")
	cache := global.String("cache", runtime.CacheDir, "repository cache")
	git := global.String("git", runtime.GitBinary, "git executable")
	global.Usage = func() {}
	if err := global.Parse(args); err != nil {
		return cliInvocation{}, cliUsageError(runtime.Err, err.Error()), true
	}
	if help {
		printUsage(runtime.Out)
		return cliInvocation{}, 0, true
	}
	if showVersion {
		printVersion(runtime.Out, runtime.Version)
		return cliInvocation{}, 0, true
	}
	command, remaining, exitCode, done := resolveCLICommand(global.Args(), runtime)
	if done {
		return cliInvocation{}, exitCode, true
	}
	explicit := make(map[string]bool)
	global.Visit(func(found *flag.Flag) { explicit[found.Name] = true })
	manifestExplicit := explicit["manifest"]
	autoUserScope := !user && !manifestExplicit && isUserAgentsDir(runtime.WorkingDir, runtime.HomeDir)
	request := ScopeRequest{
		User:             user || autoUserScope,
		ManifestPath:     *manifest,
		ManifestExplicit: manifestExplicit,
		LockPath:         *lock,
		LockExplicit:     explicit["lock"],
	}
	if request.User && request.ManifestExplicit {
		return cliInvocation{}, cliUsageError(runtime.Err, "--global/--user cannot be combined with --manifest"), true
	}
	return cliInvocation{command, *cache, *git, remaining, request, autoUserScope}, 0, false
}

func resolveCLICommand(remaining []string, runtime CLIOptions) (string, []string, int, bool) {
	if len(remaining) == 0 {
		printUsage(runtime.Out)
		return "", nil, 0, true
	}
	command := remaining[0]
	if command == "help" {
		if len(remaining) == 1 {
			printUsage(runtime.Out)
			return "", nil, 0, true
		}
		if len(remaining) == 2 {
			if spec, ok := commandSpecFor(remaining[1]); ok {
				printCommandUsage(runtime.Out, spec)
				return "", nil, 0, true
			}
			return "", nil, cliUsageError(runtime.Err, fmt.Sprintf("unknown command %q", remaining[1])), true
		}
		return "", nil, cliUsageError(runtime.Err, "help accepts at most one command"), true
	}
	spec, known := commandSpecFor(command)
	if !known {
		return "", nil, cliUsageError(runtime.Err, fmt.Sprintf("unknown command %q", command)), true
	}
	if len(remaining) == 2 && (remaining[1] == "--help" || remaining[1] == "-h") {
		printCommandUsage(runtime.Out, spec)
		return "", nil, 0, true
	}
	if spec.name == "version" {
		if len(remaining) != 1 {
			return "", nil, cliUsageError(runtime.Err, "version accepts no arguments"), true
		}
		printVersion(runtime.Out, runtime.Version)
		return "", nil, 0, true
	}
	return spec.name, remaining, 0, false
}

func runCLIInvocation(ctx context.Context, invocation cliInvocation, runtime CLIOptions) int {
	if invocation.autoUserScope {
		line := "• ~/.agents · using user scope"
		fmt.Fprintln(runtime.Out, styleUserScope(line, supportsColor(runtime.Out)))
	}
	if invocation.command == "init" {
		return runInitCLI(invocation.args, invocation.request, runtime)
	}
	layout, err := ResolveLayout(invocation.request, runtime)
	if err != nil {
		printCLIError(runtime.Err, err.Error(), runtime.HomeDir)
		return 1
	}
	lockDir := runtime.LockDir
	if lockDir == "" {
		lockDir = filepath.Join(filepath.Dir(runtime.CacheDir), "locks")
	}
	engine := NewEngine(Options{
		ProjectRoot:  layout.ProjectRoot,
		ManifestPath: layout.ManifestPath,
		LockPath:     layout.LockPath,
		TargetDir:    layout.TargetDir,
		CacheDir:     invocation.cacheDir,
		LockDir:      lockDir,
		GitBinary:    invocation.gitBinary,
	})
	return runEngineCLI(ctx, engine, invocation, runtime)
}

func runEngineCLI(ctx context.Context, engine *Engine, invocation cliInvocation, runtime CLIOptions) int {
	var err error
	switch invocation.command {
	case "sync":
		if len(invocation.args) != 1 {
			return cliUsageError(runtime.Err, "sync accepts no arguments")
		}
		var result Result
		result, err = engine.Sync(ctx)
		if err == nil {
			printResult(runtime.Out, "Sync complete", result, runtime.HomeDir, false)
		}
	case "outdated":
		var result OutdatedResult
		result, err = engine.Outdated(ctx, invocation.args[1:])
		if err == nil {
			printOutdated(runtime.Out, result, runtime.HomeDir)
		}
	case "update":
		err = runUpdateCLI(ctx, engine, invocation.args[1:], runtime.Out, runtime.HomeDir)
	case "add":
		err = runAddCLI(ctx, engine, invocation.args[1:], runtime.Out, runtime.HomeDir)
	case "remove":
		err = runRemoveCLI(ctx, engine, invocation.args[1:], runtime.Out, runtime.HomeDir)
	case "list":
		err = runListCLI(ctx, engine, invocation.args[1:], runtime.Out, runtime.HomeDir)
	case "doctor":
		var issues bool
		issues, err = runDoctorCLI(ctx, engine, invocation.args[1:], runtime.Out, runtime.HomeDir)
		if err == nil && issues {
			return 1
		}
	}
	if err != nil {
		printCLIError(runtime.Err, err.Error(), runtime.HomeDir)
		return 1
	}
	return 0
}

func runUpdateCLI(ctx context.Context, engine *Engine, args []string, output io.Writer, home string) error {
	result, err := engine.Update(ctx, args)
	if err != nil {
		return err
	}
	printFetches(output, result.Fetches, home)
	for _, change := range result.Changes {
		old := displayCommit(change.Old)
		if old == "" {
			old = "(unlocked)"
		}
		fmt.Fprintf(output, "✓ %s · %s → %s\n", displaySource(change.Source, home), old, displayCommit(change.New))
	}
	for _, local := range result.LocalSkipped {
		fmt.Fprintf(output, "✓ %s · local content synced\n", displaySource(local, home))
	}
	printResult(output, "Update complete", result, home, true)
	return nil
}

func runInitCLI(args []string, request ScopeRequest, runtime CLIOptions) int {
	if len(args) != 1 {
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
	fmt.Fprintf(runtime.Out, "✓ %s · initialized\n", displayPath(layout.ManifestPath, runtime.HomeDir))
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
		fmt.Fprintf(output, "↓ %s · fetched", displaySource(fetch.Source, home))
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
	localChanges := 0
	for _, source := range result.Sources {
		name := displaySource(source.Source, home)
		if source.Old.Commit == source.New.Commit {
			fmt.Fprintf(output, "✓ %s · up to date\n", name)
			continue
		}
		updates++
		fmt.Fprintf(output, "↑ %s · %s · update available · %s → %s\n", name, strings.Join(source.Skills, ", "), displayRevisionMetadata(source.Old), displayRevisionMetadata(source.New))
	}
	for _, local := range result.LocalSources {
		name := displaySource(local.Source, home)
		if len(local.ChangedSkills) == 0 {
			fmt.Fprintf(output, "✓ %s · up to date\n", name)
			continue
		}
		localChanges++
		fmt.Fprintf(output, "• %s · %s · local changes not synced\n", name, strings.Join(local.ChangedSkills, ", "))
	}
	if updates == 0 && localChanges == 0 {
		if len(result.Sources) == 0 && len(result.LocalSources) == 0 {
			fmt.Fprintln(output, "✓ No sources to check")
		}
		return
	}
	fmt.Fprintln(output)
	var summaries []string
	if updates > 0 {
		noun := "updates"
		if updates == 1 {
			noun = "update"
		}
		summaries = append(summaries, fmt.Sprintf("%d %s available", updates, noun))
	}
	if localChanges > 0 {
		noun := "local sources changed"
		if localChanges == 1 {
			noun = "local source changed"
		}
		summaries = append(summaries, fmt.Sprintf("%d %s", localChanges, noun))
	}
	printSummary(output, "└─ Summary · "+strings.Join(summaries, " · "))
}

func printResult(output io.Writer, label string, result Result, home string, fetchesPrinted bool) {
	if !fetchesPrinted {
		printFetches(output, result.Fetches, home)
	}
	counts := map[string]int{"installed": 0, "updated": 0, "repaired": 0, "unchanged": 0, "pruned": 0}
	names := map[string][]string{}
	for _, skill := range result.Skills {
		counts[skill.Action]++
		if skill.Action != "unchanged" {
			names[skill.Action] = append(names[skill.Action], skill.Name)
		}
	}
	actionLabels := map[string]string{"installed": "installed", "updated": "updated", "repaired": "restored", "pruned": "removed"}
	for _, action := range []string{"installed", "updated", "repaired", "pruned"} {
		for _, name := range names[action] {
			fmt.Fprintf(output, "✓ %s · %s\n", name, actionLabels[action])
		}
	}
	var summary []string
	for _, item := range []struct {
		count int
		label string
	}{
		{counts["installed"], "installed"},
		{counts["updated"], "updated"},
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
	fmt.Fprintln(output)
	printSummary(output, fmt.Sprintf("└─ %s · %s", label, strings.Join(summary, " · ")))
}

func printSummary(output io.Writer, line string) {
	fmt.Fprintln(output, styleSummary(line, supportsColor(output)))
}

func styleSummary(line string, color bool) string {
	if !color {
		return line
	}
	return "\x1b[35m" + line + "\x1b[0m"
}

func styleUserScope(line string, color bool) string {
	if !color {
		return line
	}
	return "\x1b[34m" + line + "\x1b[0m"
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
	names, result, err := engine.Add(ctx, source, parsed.skills, parsed.all, parsed.userOnly)
	if err != nil {
		return err
	}
	if len(parsed.skills) == 0 && !parsed.all && len(names) != 1 {
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
	printResult(output, "Add complete", result, home, false)
	return nil
}

type addArguments struct {
	source   string
	skills   []string
	ref      string
	all      bool
	userOnly bool
}

var skillsCLISpecifierPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([A-Za-z0-9][A-Za-z0-9._-]*)$`)

func parseAddArgs(args []string) (addArguments, error) {
	parsed, positional, err := parseAddOptions(args)
	if err != nil {
		return parsed, err
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
	if parsed.userOnly && !parsed.all && len(parsed.skills) == 0 {
		return parsed, errors.New("--invoke-user-only requires skill names or --all")
	}
	return parsed, nil
}

func parseAddOptions(args []string) (addArguments, []string, error) {
	var parsed addArguments
	var positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--all":
			parsed.all = true
		case argument == "--invoke-user-only":
			parsed.userOnly = true
		case argument == "--ref":
			index++
			if index >= len(args) {
				return parsed, positional, errors.New("--ref requires a branch, tag, or full commit hash")
			}
			parsed.ref = args[index]
		case strings.HasPrefix(argument, "--ref="):
			parsed.ref = strings.TrimPrefix(argument, "--ref=")
		case strings.HasPrefix(argument, "-"):
			return parsed, positional, fmt.Errorf("unknown add option %q", argument)
		default:
			positional = append(positional, argument)
		}
	}
	return parsed, positional, nil
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

func runListCLI(ctx context.Context, engine *Engine, args []string, output io.Writer, home string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	includeAll := flags.Bool("all", false, "include standalone unmanaged skills")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("list accepts no arguments")
	}
	var statuses []SkillStatus
	var err error
	if *includeAll {
		statuses, err = engine.ListAll(ctx)
	} else {
		statuses, err = engine.List(ctx)
	}
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
	fmt.Fprintln(writer, "SKILL\tSOURCE\tMODEL INVOCATION\tREVISION")
	fmt.Fprintln(writer, "─────\t──────\t────────────────\t────────")
	counts := make(map[string]int)
	for _, status := range statuses {
		counts[status.Status]++
		marker, _, _ := stateDisplay(status.Status)
		fmt.Fprintf(writer, "%s %s\t%s\t%s\t%s\n", marker, status.Name, displaySource(status.Source, home), displayModelInvocation(status.ModelInvocation), displayRevision(status))
	}
	if err := writer.Flush(); err != nil {
		return err
	}

	tableText := styleListHeader(table.String(), color)
	if color {
		tableText = styleListRows(tableText, statuses)
	}
	tableText = addStatusDetails(tableText, statuses)
	var result strings.Builder
	result.WriteString(tableText)
	result.WriteByte('\n')
	skillNoun := "skills"
	if len(statuses) == 1 {
		skillNoun = "skill"
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "%d %s", len(statuses), skillNoun)
	for _, state := range []string{"current", "missing", "drifted", "collision", "unlocked", "unmanaged"} {
		if counts[state] == 0 {
			continue
		}
		_, label, _ := stateDisplay(state)
		fmt.Fprintf(&summary, " · %d %s", counts[state], label)
	}
	fmt.Fprintln(&result, styleSummary("└─ "+summary.String(), color))
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
		marker, _, colorCode := stateDisplay(status.Status)
		if strings.HasPrefix(line, marker+" ") {
			line = colorCode + marker + "\x1b[0m" + line[len(marker):]
		}
		revision := displayRevision(status)
		if strings.HasSuffix(line, revision) {
			line = strings.TrimSuffix(line, revision) + "\x1b[2m" + revision + "\x1b[0m"
		}
		lines[lineIndex] = line + "\n"
	}
	return strings.Join(lines, "")
}

func addStatusDetails(table string, statuses []SkillStatus) string {
	lines := strings.SplitAfter(table, "\n")
	var result strings.Builder
	for index, line := range lines {
		result.WriteString(line)
		statusIndex := index - 2
		if statusIndex < 0 || statusIndex >= len(statuses) {
			continue
		}
		if detail := statusDetail(statuses[statusIndex].Status); detail != "" {
			fmt.Fprintf(&result, "  └─ %s\n", detail)
		}
	}
	return result.String()
}

func displayModelInvocation(invocation string) string {
	switch invocation {
	case "disabled by source":
		return "disabled"
	case "disabled by config":
		return "!disabled"
	default:
		return invocation
	}
}

func statusDetail(state string) string {
	switch state {
	case "current":
		return ""
	case "drifted":
		return "modified — managed installation has drifted or been edited"
	case "collision":
		return "blocked — target path conflicts with an unmanaged installation"
	case "missing":
		return "missing — locked skill is not installed"
	case "unlocked":
		return "unlocked — declared skill has no consistent lock entry"
	case "unmanaged":
		return ""
	default:
		return state
	}
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
	case "unmanaged":
		return "•", "unmanaged", "\x1b[36m"
	default:
		return "?", state, "\x1b[33m"
	}
}

func runDoctorCLI(ctx context.Context, engine *Engine, args []string, output io.Writer, home string) (bool, error) {
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
		fmt.Fprintln(output, "✓ No issues found")
	} else {
		for _, issue := range report.Issues {
			label := issue.Kind
			if issue.Skill != "" {
				label += "/" + issue.Skill
			}
			fmt.Fprintf(output, "! %s · %s\n", label, displayPathsInText(issue.Message, home))
		}
	}
	return len(report.Issues) > 0, nil
}

func printVersion(output io.Writer, version string) {
	if version == "" {
		version = "dev"
	}
	fmt.Fprintf(output, "skillsrc %s\n", version)
}

func cliUsageError(output io.Writer, message string) int {
	fmt.Fprintf(output, "Error: %s\n\n", message)
	fmt.Fprintln(output, "Usage: skillsrc [OPTIONS] <COMMAND>")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "For more information, run 'skillsrc --help'.")
	return 2
}

type cliCommandSpec struct {
	name, description, usage, arguments, options string
	aliases                                      []string
}

const globalOptionsHelp = `  -g, --global       Use ~/.agents/skills.toml
      --user          Alias for --global
      --manifest PATH Use the skills.toml at PATH instead of searching parent directories
      --lock PATH     Override the lockfile path
      --cache PATH    Override the Git repository cache
      --git PATH      Override the Git executable
  -h, --help          Print help
      --version       Print version`

var cliCommandSpecs = []cliCommandSpec{
	{"init", "Initialize a manifest", "skillsrc [OPTIONS] init", "None.", "None.", nil},
	{"sync", "Install the exact declared and locked skill set", "skillsrc [OPTIONS] sync", "None.", "None.", nil},
	{"add", "Add skills from a Git repository or local directory", "skillsrc [OPTIONS] add [OPTIONS] <SOURCE> [SKILL...]", "<SOURCE>     Repository or local directory. SOURCE@SKILL selects one skill.\n[SKILL...]   Skill names to add. Omit to install the sole skill or list multiple choices.", "--all                 Add every discovered skill.\n--ref REF             Git branch, tag, or full commit hash.\n--invoke-user-only    Disable model invocation for added skills.", nil},
	{"remove", "Remove skills and their managed installations", "skillsrc [OPTIONS] remove <SKILL>...", "<SKILL>...  One or more skill names.", "None.", []string{"rm"}},
	{"outdated", "Show Git updates and local changes without changing project files", "skillsrc [OPTIONS] outdated [SOURCE|SKILL...]", "[SOURCE|SKILL...]  Sources or skill names to check; defaults to all sources.", "None.", nil},
	{"update", "Update Git revisions, then sync", "skillsrc [OPTIONS] update [SOURCE|SKILL...]", "[SOURCE|SKILL...]  Sources or skill names to update; defaults to all sources.", "None.", nil},
	{"list", "Show configured skills and installation state", "skillsrc [OPTIONS] list [OPTIONS]", "None.", "--all   Include standalone unmanaged skills.\n--json  Print JSON.", []string{"ls"}},
	{"doctor", "Diagnose or repair lock, install, cache, and project metadata", "skillsrc [OPTIONS] doctor [OPTIONS]", "None.", "--repair  Repair issues by running sync.\n--json    Print JSON.", nil},
	{"version", "Print version", "skillsrc version", "None.", "None.", nil},
}

func commandSpecFor(name string) (cliCommandSpec, bool) {
	for _, spec := range cliCommandSpecs {
		if name == spec.name {
			return spec, true
		}
		for _, alias := range spec.aliases {
			if name == alias {
				return spec, true
			}
		}
	}
	return cliCommandSpec{}, false
}

func printUsage(output io.Writer) {
	var commands strings.Builder
	for _, spec := range cliCommandSpecs {
		fmt.Fprintf(&commands, "  %-9s %s", spec.name, spec.description)
		if len(spec.aliases) > 0 {
			fmt.Fprintf(&commands, " [alias: %s]", strings.Join(spec.aliases, ", "))
		}
		commands.WriteByte('\n')
	}
	fmt.Fprintf(output, strings.TrimSpace(`skillsrc — Your .agents-only skills manager. No abstraction theater.

Usage: skillsrc [OPTIONS] <COMMAND>

Commands:
%s  help      Print general or command help

Scope selection:
  By default, skillsrc uses the nearest project skills.toml.
  When run directly from ~/.agents, skillsrc uses the user scope.

  -g, --global       Use ~/.agents/skills.toml
      --user         Alias for --global
      --manifest PATH
                     Use the skills.toml at PATH instead of searching parent directories

Options:
      --lock PATH    Override the lockfile path
      --cache PATH   Override the Git repository cache
      --git PATH     Override the Git executable
  -h, --help         Print help
      --version      Print version

Run 'skillsrc help <COMMAND>' for command details.`), commands.String())
	fmt.Fprintln(output)
}

func printCommandUsage(output io.Writer, spec cliCommandSpec) {
	fmt.Fprintf(output, "%s — %s\n\nUsage: %s\n", spec.name, spec.description, spec.usage)
	if len(spec.aliases) > 0 {
		fmt.Fprintf(output, "\nAliases: %s\n", strings.Join(spec.aliases, ", "))
	}
	fmt.Fprintf(output, "\nArguments:\n%s\n\nOptions:\n%s\n\nGlobal options:\n%s\n", indentHelpLines(spec.arguments), indentHelpLines(spec.options), globalOptionsHelp)
}

func indentHelpLines(text string) string {
	return "  " + strings.ReplaceAll(text, "\n", "\n  ")
}
