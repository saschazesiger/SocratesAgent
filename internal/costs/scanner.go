package costs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Version is part of every fingerprint: a build that prices or reads
// differently computes every ledger again rather than trusting one an older
// reader made.
const Version = "1"

// Root is one account's directories: the default profile's, or a profile's.
// An empty directory is skipped.
type Root struct {
	Profile     string
	ClaudeDir   string
	CodexHome   string
	OpenCodeDir string
}

// Conversation is one conversation's ledger as the transcripts on this disk
// have it now.
type Conversation struct {
	Harness string
	ID      string
	Profile string
	// Cwd is the directory the CLI worked in, and Title the name the CLI gave
	// the conversation, when it keeps one.
	Cwd   string
	Title string
	// Fingerprint names the state of the files the ledger was read from.
	Fingerprint     string
	FirstAt, LastAt int64
	Rows            []store.CostRow
}

// Key is the conversation's ledger key.
func (c Conversation) Key() string { return store.CostKey(c.Harness, c.ID) }

// Scanner reads the transcripts and remembers enough of them that the next
// pass reads only what was appended. It is safe for one pass at a time.
type Scanner struct {
	mu       sync.Mutex
	claude   map[string]*claudeFile
	codex    map[string]*codexFile
	opencode map[string]string // database path -> the fingerprint last read
	// keep is how long a file that has not changed stays parsed in memory.
	keep time.Duration
}

// NewScanner returns an empty scanner.
func NewScanner() *Scanner {
	return &Scanner{
		claude:   map[string]*claudeFile{},
		codex:    map[string]*codexFile{},
		opencode: map[string]string{},
		keep:     15 * time.Minute,
	}
}

// Scan reads every root and returns the conversations whose files changed
// since the fingerprint in seen (keyed by ledger key). A conversation that
// cost nothing - a transcript with no answer in it - is not returned.
func (sc *Scanner) Scan(roots []Root, seen map[string]string) []Conversation {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var out []Conversation
	taken := map[string]bool{}
	add := func(list []Conversation) {
		for _, c := range list {
			// The same conversation under two roots is one conversation; a
			// profile that links another's directory must not count it twice.
			if taken[c.Key()] || len(c.Rows) == 0 {
				continue
			}
			taken[c.Key()] = true
			out = append(out, c)
		}
	}
	for _, root := range roots {
		if root.ClaudeDir != "" {
			add(sc.scanClaude(root, seen))
		}
		if root.CodexHome != "" {
			add(sc.scanCodex(root, seen))
		}
		if root.OpenCodeDir != "" {
			add(sc.scanOpenCode(root, seen))
		}
	}
	sc.evict()
	return out
}

// evict drops the parsed contents of files that have not changed for a
// while. Their size and time stay, which is all a pass needs to see that
// they are still the same.
func (sc *Scanner) evict() {
	cutoff := time.Now().Add(-sc.keep).UnixNano()
	for _, f := range sc.claude {
		if f.mod < cutoff {
			f.drop()
		}
	}
	for _, f := range sc.codex {
		if f.mod < cutoff {
			f.recs = nil
			f.parsed = false
		}
	}
}

// fileState is a file's identity for a fingerprint.
type fileState struct {
	path string
	size int64
	mod  int64
}

func statFile(path string) (fileState, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileState{}, false
	}
	return fileState{path: path, size: info.Size(), mod: info.ModTime().UnixNano()}, true
}

func fingerprint(states []fileState) string {
	sort.Slice(states, func(i, j int) bool { return states[i].path < states[j].path })
	h := sha256.New()
	fmt.Fprintf(h, "v%s\n", Version)
	for _, s := range states {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", s.path, s.size, s.mod)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// spend is one request, or a share of a correction, on the way into hours.
type spend struct {
	at                                   int64 // Unix milliseconds
	model                                string
	input, output, cacheRead, cacheWrite float64
	cost                                 float64
	priced                               bool
	requests                             int64
}

// hourly sums spends into the ledger's rows, and reports the first and last
// moment it saw.
func hourly(spends []spend) (rows []store.CostRow, first, last int64) {
	type slot struct {
		hour  int64
		model string
	}
	type sums struct {
		cost                                 float64
		input, output, cacheRead, cacheWrite float64
		requests, unpriced                   int64
	}
	acc := map[slot]*sums{}
	for _, s := range spends {
		if s.at <= 0 {
			continue
		}
		if first == 0 || s.at < first {
			first = s.at
		}
		if s.at > last {
			last = s.at
		}
		k := slot{s.at / 3_600_000, s.model}
		a := acc[k]
		if a == nil {
			a = &sums{}
			acc[k] = a
		}
		a.cost += s.cost
		a.input += s.input
		a.output += s.output
		a.cacheRead += s.cacheRead
		a.cacheWrite += s.cacheWrite
		a.requests += s.requests
		if !s.priced && s.requests > 0 {
			a.unpriced += s.requests
		}
	}
	for k, a := range acc {
		rows = append(rows, store.CostRow{
			Hour: k.hour, Model: k.model, CostUSD: a.cost,
			Input: int64(math.Round(a.input)), Output: int64(math.Round(a.output)),
			CacheRead: int64(math.Round(a.cacheRead)), CacheWrite: int64(math.Round(a.cacheWrite)),
			Requests: a.requests, Unpriced: a.unpriced,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Hour != rows[j].Hour {
			return rows[i].Hour < rows[j].Hour
		}
		return rows[i].Model < rows[j].Model
	})
	return rows, first, last
}
