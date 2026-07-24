package skillsrc

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

func RunCLI(ctx context.Context, args []string, options Options) int {
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.Err == nil {
		options.Err = io.Discard
	}
	global := flag.NewFlagSet("skillsrc", flag.ContinueOnError)
	global.SetOutput(options.Err)
	manifest := global.String("manifest", options.ManifestPath, "manifest path (default ~/.agents/skills.toml)")
	lock := global.String("lock", "", "lockfile path (default beside manifest)")
	target := global.String("target", options.TargetDir, "installation target")
	cache := global.String("cache", options.CacheDir, "repository cache")
	git := global.String("git", options.GitBinary, "git executable")
	global.Usage = func() { printUsage(options.Err) }
	if err := global.Parse(args); err != nil {
		return 2
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		printUsage(options.Err)
		return 2
	}
	manifestChanged := *manifest != global.Lookup("manifest").DefValue
	options.ManifestPath = *manifest
	if *lock != "" {
		options.LockPath = *lock
	} else if manifestChanged || options.LockPath == "" {
		options.LockPath = filepath.Join(filepath.Dir(*manifest), "skills.lock")
	}
	options.TargetDir, options.CacheDir, options.GitBinary = *target, *cache, *git
	engine := NewEngine(options)

	var err error
	switch remaining[0] {
	case "sync":
		if len(remaining) != 1 {
			return cliUsageError(options.Err, "sync accepts no arguments")
		}
		var result Result
		result, err = engine.Sync(ctx)
		if err == nil {
			printResult(options.Out, "sync complete", result)
		}
	case "update":
		var result Result
		result, err = engine.Update(ctx, remaining[1:])
		if err == nil {
			for _, change := range result.Changes {
				old := change.Old
				if old == "" {
					old = "(unlocked)"
				}
				fmt.Fprintf(options.Out, "%s: %s -> %s\n", change.Source, old, change.New)
			}
			for _, local := range result.LocalSkipped {
				fmt.Fprintf(options.Out, "%s: local (no remote version)\n", local)
			}
			printResult(options.Out, "update complete", result)
		}
	case "add":
		err = runAddCLI(ctx, engine, remaining[1:], options.Out)
	case "remove", "rm":
		err = runRemoveCLI(ctx, engine, remaining[1:], options.Out)
	case "list", "ls":
		err = runListCLI(ctx, engine, remaining[1:], options.Out, options.Err)
	case "doctor":
		var issues bool
		issues, err = runDoctorCLI(ctx, engine, remaining[1:], options.Out, options.Err)
		if err == nil && issues {
			return 1
		}
	case "help":
		printUsage(options.Out)
		return 0
	default:
		return cliUsageError(options.Err, fmt.Sprintf("unknown command %q", remaining[0]))
	}
	if err != nil {
		fmt.Fprintf(options.Err, "skillsrc: %v\n", err)
		return 1
	}
	return 0
}

func printFetches(output io.Writer, fetches []FetchEvent) {
	for _, fetch := range fetches {
		commit := ""
		if fetch.Commit != "" {
			commit = " (" + fetch.Commit + ")"
		}
		fmt.Fprintf(output, "fetched %s: %s%s\n", fetch.Source, fetch.Reason, commit)
	}
}

func printResult(output io.Writer, label string, result Result) {
	printFetches(output, result.Fetches)
	counts := map[string]int{"installed": 0, "repaired": 0, "unchanged": 0, "pruned": 0}
	names := map[string][]string{}
	for _, skill := range result.Skills {
		counts[skill.Action]++
		if skill.Action != "unchanged" {
			names[skill.Action] = append(names[skill.Action], skill.Name)
		}
	}
	for _, action := range []string{"installed", "repaired", "pruned"} {
		if len(names[action]) > 0 {
			fmt.Fprintf(output, "%s: %s\n", action, strings.Join(names[action], ", "))
		}
	}
	fmt.Fprintf(output, "%s: %d installed, %d repaired, %d unchanged, %d pruned; %d repositories fetched\n", label, counts["installed"], counts["repaired"], counts["unchanged"], counts["pruned"], len(result.Fetches))
}

func runAddCLI(ctx context.Context, engine *Engine, args []string, output io.Writer) error {
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
		printFetches(output, result.Fetches)
		fmt.Fprintf(output, "Available skills in %s:\n", parsed.source)
		for _, name := range names {
			fmt.Fprintf(output, "  %s\n", name)
		}
		return nil
	}
	_, result, err := engine.Add(ctx, source, parsed.skills, parsed.all)
	if err != nil {
		return err
	}
	printResult(output, "add complete", result)
	return nil
}

type addArguments struct {
	source string
	skills []string
	ref    string
	all    bool
	list   bool
}

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
	if parsed.all && len(parsed.skills) > 0 {
		return parsed, errors.New("--all cannot be combined with skill names")
	}
	if parsed.list && (parsed.all || len(parsed.skills) > 0) {
		return parsed, errors.New("--list cannot be combined with --all or skill names")
	}
	return parsed, nil
}

func runRemoveCLI(ctx context.Context, engine *Engine, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("remove requires at least one skill name")
	}
	result, err := engine.Remove(ctx, args)
	if err != nil {
		return err
	}
	printResult(output, "remove complete", result)
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

func runListCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
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
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "SKILL\tSOURCE\tREVISION\tSTATE")
	counts := make(map[string]int)
	for _, status := range statuses {
		counts[status.Status]++
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", status.Name, displaySource(status.Source), displayRevision(status), displayState(status.Status))
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%d skills", len(statuses))
	for _, state := range []string{"current", "missing", "drifted", "collision", "unlocked"} {
		if counts[state] == 0 {
			continue
		}
		label := state
		if state == "current" {
			label = "synced with skills.lock"
		} else if state == "drifted" {
			label = "modified"
		} else if state == "collision" {
			label = "blocked"
		}
		fmt.Fprintf(writer, "  ·  %d %s", counts[state], label)
	}
	fmt.Fprintln(writer)
	return writer.Flush()
}

func displaySource(source string) string {
	for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/", "https://github.com/"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimSuffix(strings.TrimPrefix(source, prefix), ".git")
		}
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

func displayState(state string) string {
	switch state {
	case "current":
		return "✓ synced"
	case "drifted":
		return "! modified"
	case "collision":
		return "! blocked"
	case "missing":
		return "✗ missing"
	default:
		return "? " + state
	}
}

func runDoctorCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer) (bool, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
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
		fmt.Fprintln(output, "ok")
	} else {
		for _, issue := range report.Issues {
			label := issue.Kind
			if issue.Skill != "" {
				label += "/" + issue.Skill
			}
			fmt.Fprintf(output, "%s: %s\n", label, issue.Message)
		}
	}
	return len(report.Issues) > 0, nil
}

func cliUsageError(output io.Writer, message string) int {
	fmt.Fprintf(output, "skillsrc: %s\n", message)
	printUsage(output)
	return 2
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, strings.TrimSpace(`skillsrc - Declarative skill dependencies for .agents/skills.

Usage:
  skillsrc [global flags] sync
  skillsrc [global flags] add SOURCE [SKILL ...] [--all] [--list] [--ref REF]
  skillsrc [global flags] remove SKILL ...
  skillsrc [global flags] update [source-or-skill ...]
  skillsrc [global flags] list [--json]
  skillsrc [global flags] doctor [--repair] [--json]

Global flags:
  --manifest PATH  manifest (default ~/.agents/skills.toml)
  --lock PATH      lockfile (default skills.lock beside manifest)
  --target PATH    installation directory (default ~/.agents/skills)
  --cache PATH     Git repository cache (default user cache/skillsrc/repos)`))
}
