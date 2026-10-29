package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A chat sends at most chatSendsPerHour messages an hour.
func TestAChatsSendsAreCounted(t *testing.T) {
	tools := &chatToolServer{sends: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < chatSendsPerHour; i++ {
		if !tools.allowSend("a", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("send %d refused", i+1)
		}
	}
	if tools.allowSend("a", now.Add(time.Minute)) {
		t.Fatal("one more than the hour allows went")
	}
	if !tools.allowSend("b", now.Add(time.Minute)) {
		t.Fatal("another chat's send was refused")
	}
	if !tools.allowSend("a", now.Add(time.Hour+time.Second)) {
		t.Fatal("an hour on, the chat still could not send")
	}
}

// The branch is read from the working tree's HEAD, a linked worktree's too,
// and there is none for a detached HEAD or a folder that is no repository.
func TestGitBranch(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	if got := gitBranch(main); got != "main" {
		t.Fatalf("main: %q", got)
	}
	wt := t.TempDir()
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	write(filepath.Join(main, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/feature/x\n")
	if got := gitBranch(wt); got != "feature/x" {
		t.Fatalf("worktree: %q", got)
	}
	write(filepath.Join(main, ".git", "HEAD"), "4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
	if got := gitBranch(main); got != "" {
		t.Fatalf("detached: %q", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Fatalf("no repository: %q", got)
	}
}

// The repository of a folder is the nearest folder with a .git, else the
// folder itself.
func TestChatScope(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := chatScope(filepath.Join(repo, "a", "b")); got != repo {
		t.Fatalf("below a repository: %s", got)
	}
	plain := t.TempDir()
	if got := chatScope(plain); got != plain {
		t.Fatalf("no repository: %s", got)
	}
	if chatScope("") != "" {
		t.Fatal("no folder has a scope")
	}
}
