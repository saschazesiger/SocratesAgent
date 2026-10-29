package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// testsCommand is `socrates tests`: the test catalog of a repository from a
// terminal or a hook (TEST-MODE.md, "Validation").
func testsCommand(args []string) int {
	return testsMain(args, os.Stdout, os.Stderr)
}

// testsSubcommands are the words after `socrates tests`, each with its
// one-line help and its function.
var testsSubcommands = []struct {
	name, args, help string
	run              func(args []string, stdout, stderr io.Writer) int
}{
	{"check", "[repository]", "validate the repository's .socrates/tests (default: the git repository around the current folder)", testsCheck},
}

// testsMain is testsCommand with its output in the caller's hands.
func testsMain(args []string, stdout, stderr io.Writer) int {
	usage := func(w io.Writer) {
		fmt.Fprintf(w, "Usage:\n")
		for _, sc := range testsSubcommands {
			fmt.Fprintf(w, "  socrates tests %s %s\n      %s\n", sc.name, sc.args, sc.help)
		}
	}
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	for _, sc := range testsSubcommands {
		if args[0] == sc.name {
			return sc.run(args[1:], stdout, stderr)
		}
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "socrates tests: %q is not a command.\n\n", args[0])
	usage(stderr)
	return 2
}

// testsCheck is `socrates tests check [repository]`: every error and
// warning of the catalog as file:line: message (paths from the repository's
// root, as an editor opens them), then a summary. It exits 1 on errors,
// as a run would not start on them.
func testsCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("socrates tests check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage:\n  socrates tests check [repository]\n\n"+
			"Validates the repository's test catalog (%s): the errors that stop a run and the warnings that do not.\n"+
			"Without a repository it checks the git repository around the current folder.\n", catalog.DirName)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	repo := fs.Arg(0)
	if repo == "" {
		repo = repositoryRoot()
	}
	c, err := catalog.Load(repo)
	if err != nil {
		fmt.Fprintf(stderr, "socrates tests check: %v\n", err)
		return 1
	}
	show := func(p catalog.Problem, kind string) {
		p.File = catalog.DirName + "/" + p.File
		if kind != "" {
			p.Message = kind + ": " + p.Message
		}
		fmt.Fprintln(stdout, p.String())
	}
	for _, p := range c.Errors {
		show(p, "")
	}
	for _, p := range c.Warnings {
		show(p, "warning")
	}
	summary := fmt.Sprintf("%s, %s, %s", plural(len(c.Areas), "area"), plural(c.TestCount(), "test"), plural(len(c.Cells()), "cell"))
	if len(c.Errors) > 0 || len(c.Warnings) > 0 {
		summary += fmt.Sprintf("; %s, %s", plural(len(c.Errors), "error"), plural(len(c.Warnings), "warning"))
	}
	fmt.Fprintln(stdout, summary)
	if len(c.Errors) > 0 {
		return 1
	}
	return 0
}

// repositoryRoot is the git repository around the current folder (the
// nearest one holding a .git, a folder or a worktree's file), or the
// current folder when there is none.
func repositoryRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for dir := wd; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
