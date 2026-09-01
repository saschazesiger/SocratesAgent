// Package piper reads an answer out loud on the machine Socrates runs on.
//
// Piper is a small neural text to speech engine that runs on the CPU: no key,
// no account, no per-character bill and no round trip to a provider that may
// not be reachable from a moving car. It is the only way Socrates speaks,
// which is why this package installs it itself - the published binary plus one
// voice per spoken language - instead of asking anyone to put it there first.
//
// Rendering is one process per request. Loading a model costs about a quarter
// of a second and the rest is arithmetic, so a resident process would buy
// little and cost the ability to kill a render nobody is waiting for any more.
package piper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ContentType is what Speak returns beside the audio. Piper writes a complete
// RIFF WAV to stdout, header and all, so nothing here has to build one.
const ContentType = "audio/wav"

// The two voices Socrates installs, one per spoken language.
//
// Both are chosen for their licence as much as for how they sound: this is a
// public source project that ships a Docker image, so a model that may not be
// redistributed is a model this app cannot use. Thorsten is CC0, which is as
// clean as it gets. LJSpeech is trained from scratch on the public domain
// LJSpeech corpus - unlike the otherwise popular amy, which is a fine-tune of
// lessac and so inherits the research-only terms of the Blizzard 2013 data,
// with a model card that points at a repository not listing it at all.
//
// Medium is the quality that fits: the low models sound like a phone menu and
// the high ones cost three times the download for a difference nobody hears
// through a car speaker.
const (
	VoiceEnglish = "en_US-ljspeech-medium"
	VoiceGerman  = "de_DE-thorsten-medium"
)

// States the setup check in the dashboard renders.
const (
	StateReady      = "ready"
	StateInstalling = "installing"
	StateMissing    = "missing"
	StateFailed     = "failed"
)

// Where a piper came from, which is what the setup check reports so that a
// surprising voice can be traced to the binary producing it.
const (
	sourceBaked   = "baked-in"
	sourcePath    = "PATH"
	sourceManaged = "managed"
)

// maxTextRunes is a backstop rather than the real limit: the caller trims an
// answer to something a listener will sit through long before it arrives here.
// It exists so a runaway answer cannot turn into ten minutes of rendering that
// nobody asked for.
const maxTextRunes = 6000

// renderTimeout is generous on purpose. Six thousand characters take a couple
// of seconds on a laptop and the better part of a minute on the kind of ARM
// board this app also runs on, and a render that is merely slow should be
// allowed to finish.
const renderTimeout = 5 * time.Minute

// wavHeaderBytes is the size of the canonical RIFF header piper writes.
// Anything shorter cannot be audio, whatever it starts with.
const wavHeaderBytes = 44

// Status is the snapshot the dashboard's setup check shows.
type Status struct {
	Ready  bool     `json:"ready"`
	State  string   `json:"state"`
	Detail string   `json:"detail"`
	Path   string   `json:"path"`
	Voices []string `json:"voices"`
	Err    string   `json:"error,omitempty"`
}

// Engine is the voice: one per server. The URL fields are not a choice the app
// offers anyone, only the seam that lets the tests put a stand in where GitHub
// and Hugging Face would be.
type Engine struct {
	Dir        string
	ReleaseURL string
	VoicesURL  string
	HTTP       *http.Client

	mu         sync.Mutex
	installing bool
	// done closes when the install that is running ends. It is how a caller
	// joins one without being the thing that keeps it alive.
	done     chan struct{}
	progress string
	lastErr  error
}

// New builds an engine that keeps its installation in dir. The caller passes a
// directory Socrates owns, usually <dataDir>/voice.
func New(dir string) *Engine {
	return &Engine{
		Dir:        dir,
		ReleaseURL: DefaultReleaseURL,
		VoicesURL:  DefaultVoicesURL,
		// The three files come to about 150 MB and the machine may well be
		// behind a phone. The timeout is there to end a connection that died
		// without saying so, not to put a ceiling on a slow one.
		HTTP: &http.Client{Timeout: 45 * time.Minute},
	}
}

// Ready reports whether the engine can speak right now.
//
// It looks at the filesystem every time rather than remembering an answer,
// because an installation can appear or disappear underneath a running server
// - an image that mounts the voices late, a piper someone just brew installed,
// a disk that got cleaned up - and none of those should need a restart to be
// noticed.
func (e *Engine) Ready() bool { return e.resolve().ready() }

// ErrInstalling says the engine cannot speak yet because the 150 MB it needs
// are already on their way. It is a distinct error because the caller that
// matters is a browser waiting for an answer: it has to be told to come back
// in a moment rather than left holding a connection open for the length of a
// download.
var ErrInstalling = errors.New("piper is still being installed")

// Ensure makes the engine ready and waits for it, which the installer at
// startup wants and a render must never do. It is safe to call from anywhere
// and from several places at once: a second caller joins the first install
// rather than starting another.
//
// Waiting is all this adds. The install itself runs on the engine's own
// context either way, so a caller that leaves - a cancelled context, a
// goroutine that gave up - leaves the download running.
func (e *Engine) Ensure(ctx context.Context) error {
	if e.Ready() {
		return nil
	}
	select {
	case <-e.startInstall():
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// installTimeout is a ceiling rather than a budget: longer than any honest
// 150 MB over a phone connection, short enough that a transfer which died
// without saying so cannot keep a goroutine for the life of the server.
const installTimeout = 2 * time.Hour

// startInstall makes sure an install is running and hands back the channel that
// closes when it ends. It is the only way one ever begins, and that is the
// point: an install driven by a request's context dies when the browser gives
// up on its answer, so the twenty seconds a page waits before it says the voice
// did not answer were also twenty seconds of download, thrown away on every
// press. The 25 MB binary fits in that window and the two 60 MB voices never
// can, which is a voice that cannot finish installing itself at all.
func (e *Engine) startInstall() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.installing {
		// Whoever asked first is already downloading exactly this, and a second
		// transfer would only compete with it for the connection.
		return e.done
	}
	e.installing = true
	e.progress = ""
	// lastErr is deliberately left standing until this attempt has its own
	// answer: while a retry runs it is the only thing that tells a first
	// install which is merely slow apart from one that has already failed
	// once, and the reason a retry will most likely fail again with.
	done := make(chan struct{})
	e.done = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
		defer cancel()
		err := e.install(ctx)
		e.finish(err)
		close(done)
	}()
	return done
}

// Speak renders text and returns the audio with its content type. language is
// "en" or "de"; rate is the speaking rate where 1 is normal and anything at or
// below 0 means normal.
func (e *Engine) Speak(ctx context.Context, text, language string, rate float64) ([]byte, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		// An empty WAV is indistinguishable from a voice that broke, so the
		// caller hears about this instead of being handed silence.
		return nil, "", errors.New("there is no text to read out loud")
	}
	if runes := []rune(text); len(runes) > maxTextRunes {
		// Cut on a rune so the text ends in a word rather than in half of a
		// UTF-8 sequence, which piper would read out as a stray character.
		text = string(runes[:maxTextRunes])
	}
	// A render never installs anything itself. When the engine is not ready it
	// starts the download on the engine's own context and bows out at once: the
	// listener is waiting now, 150 MB will not be here now, and an install this
	// request owned would be cancelled by the very browser that is waiting for
	// it.
	if !e.Ready() {
		e.startInstall()
		return nil, "", ErrInstalling
	}

	inst := e.resolve()
	voice := voiceFor(language)

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	args := []string{
		"--quiet",
		"--model", modelPath(inst.voices, voice),
		// The config defaults to the model path plus .json, which is exactly
		// the layout here. Naming it anyway means a voice whose config went
		// missing fails with a sentence about that file rather than with
		// piper looking somewhere else.
		"--config", configPath(inst.voices, voice),
	}
	if inst.tree != "" {
		// espeak-ng turns the text into phonemes and looks for its data at an
		// absolute path baked in when it was built, which is not where the
		// standalone release puts it. Passing the path is what makes the
		// extracted tree work from wherever it happens to sit. A piper that
		// came from the PATH was installed by a package manager that put the
		// data where its own build expects it, so it is left alone.
		args = append(args, "--espeak_data", espeakData(inst.tree))
	}
	args = append(args, "--output_file", "-")
	if scale, ok := lengthScale(rate); ok {
		args = append(args, "--length_scale", scale)
	}

	cmd := exec.CommandContext(ctx, inst.binary, args...)
	cmd.Env = childEnv(inst.tree)
	cmd.Stdin = strings.NewReader(text)
	var audio bytes.Buffer
	stderr := &capped{limit: 8 << 10}
	cmd.Stdout = &audio
	cmd.Stderr = stderr
	// A piper that dies early stops reading stdin, which leaves the goroutine
	// copying the text into it blocked on a pipe nobody drains. WaitDelay is
	// what turns that into a call that returns rather than a stuck handler.
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, "", fmt.Errorf("piper did not finish reading the text within %s", renderTimeout)
		}
		return nil, "", fmt.Errorf("piper could not read the text: %v: %s", err, flatten(stderr.String()))
	}
	out := audio.Bytes()
	if len(out) < wavHeaderBytes || !bytes.HasPrefix(out, []byte("RIFF")) {
		// Piper can exit zero and still have printed its complaint instead of
		// audio, so what came back is the only honest test of whether it
		// worked.
		return nil, "", fmt.Errorf("piper returned %d bytes that are not a WAV file: %s", len(out), flatten(stderr.String()))
	}
	return out, ContentType, nil
}

// Status reports what the engine can do and, while an install runs, how far it
// has got.
func (e *Engine) Status() Status {
	inst := e.resolve()
	status := Status{Path: inst.binary, Voices: installedVoices(inst.voices)}
	if status.Path == "" {
		status.Path = binaryPath(e.Dir)
	}

	e.mu.Lock()
	installing, progress, lastErr := e.installing, e.progress, e.lastErr
	e.mu.Unlock()

	switch {
	case inst.ready():
		status.Ready = true
		status.State = StateReady
		switch inst.source {
		case sourceBaked:
			status.Detail = "Piper is ready, using the installation baked in at " + inst.binary + "."
		case sourcePath:
			status.Detail = "Piper is ready, using the one found on the PATH at " + inst.binary + "."
		default:
			status.Detail = "Piper is ready, using the copy Socrates installed at " + inst.binary + "."
		}
	case installing:
		status.State = StateInstalling
		status.Detail = progress
		if status.Detail == "" {
			status.Detail = "Piper is being installed."
		}
		if lastErr != nil {
			// An earlier attempt failed and this one is the retry. Carrying
			// its reason along is what keeps a platform where the install can
			// never work from reading as a download that is simply slow.
			status.Err = lastErr.Error()
			status.Detail = "Installing Piper failed once and is being tried again. " + status.Detail
		}
	case lastErr != nil:
		status.State = StateFailed
		status.Err = lastErr.Error()
		status.Detail = "Installing Piper failed, it is tried again the next time an answer is read out loud."
	case inst.binary != "":
		status.State = StateMissing
		status.Detail = "Piper is installed at " + inst.binary +
			" but its voices are not, they are downloaded the first time an answer is read out loud."
	default:
		status.State = StateMissing
		status.Detail = "Piper is not installed yet, it downloads itself the first time an answer is read out loud."
		if _, err := assetFor(runtime.GOOS, runtime.GOARCH); err != nil {
			// There is no build to fetch on this platform, and saying so here
			// is the difference between a setup check that reads as "wait a
			// moment" and one that reads as "do this".
			status.Detail = err.Error()
		}
	}
	return status
}

// finish records how the install ended, for the setup check to show.
func (e *Engine) finish(err error) {
	e.mu.Lock()
	e.installing = false
	e.lastErr = err
	if err == nil {
		e.progress = ""
	}
	e.mu.Unlock()
}

// report records what the install is doing, so the setup check has something
// to show while 150 MB are on their way over a phone connection.
func (e *Engine) report(line string) {
	e.mu.Lock()
	e.progress = line
	e.mu.Unlock()
}

// installation is the piper one render will use, and where its voices come
// from. They are resolved separately because the two halves can honestly come
// from different places - a piper installed by a package manager has no voices
// with it, and Socrates keeps its own.
type installation struct {
	binary string // the executable to run, empty when there is none yet
	tree   string // the extracted release holding the bundled libraries and espeak data, empty for a piper from the PATH
	voices string // the directory holding the .onnx models
	source string
}

func (i installation) ready() bool { return i.binary != "" && voicesInstalled(i.voices) }

// resolve finds the piper to run, in the order that respects what is already
// on the machine:
//
//  1. the installation SOCRATES_PIPER_DIR points at. This is how the Docker
//     image ships with piper and both voices already inside it, so the first
//     answer is read out loud immediately instead of after a download. It is
//     used read only.
//  2. a piper on the PATH, which is what `brew install piper` or a distro
//     package leaves behind. It is taken at its word rather than probed on
//     every status poll; a binary that turns out to be something else shows
//     up as a render error quoting whatever it said. This branch is also what
//     makes macOS work at all - see assetFor.
//  3. the copy Socrates downloads and manages itself.
//
// The voices are resolved the same way and separately: a baked in directory
// that carries both is used, otherwise they are the managed ones, because a
// binary from the PATH or a read only image directory brings none.
func (e *Engine) resolve() installation {
	inst := installation{voices: voiceDir(e.Dir), source: sourceManaged}

	baked := strings.TrimSpace(os.Getenv(EnvDir))
	if baked != "" && voicesInstalled(voiceDir(baked)) {
		inst.voices = voiceDir(baked)
	}

	switch {
	case baked != "" && binaryInstalled(baked):
		inst.binary, inst.tree, inst.source = binaryPath(baked), binaryDir(baked), sourceBaked
	case binaryInstalled(e.Dir):
		// The managed copy comes before the PATH once it exists: it is the one
		// this package downloaded, verified and knows the layout of.
		inst.binary, inst.tree = binaryPath(e.Dir), binaryDir(e.Dir)
	default:
		if found, err := exec.LookPath("piper"); err == nil {
			inst.binary, inst.source = found, sourcePath
		}
	}
	return inst
}

// The layout inside an installation root: the extracted release in piper/ and
// the models in voices/. A baked in directory is laid out exactly this way,
// which is what lets one set of paths serve both.
func binaryDir(root string) string  { return filepath.Join(root, "piper") }
func binaryPath(root string) string { return filepath.Join(binaryDir(root), exeName()) }
func voiceDir(root string) string   { return filepath.Join(root, "voices") }
func espeakData(tree string) string { return filepath.Join(tree, "espeak-ng-data") }

func modelPath(voices, voice string) string  { return filepath.Join(voices, voice+".onnx") }
func configPath(voices, voice string) string { return modelPath(voices, voice) + ".json" }

func exeName() string {
	if runtime.GOOS == "windows" {
		return "piper.exe"
	}
	return "piper"
}

// binaryInstalled reports whether an extracted tree under root is whole. The
// espeak data counts because it is named on every command line: a tree without
// it starts and then refuses every sentence, which looks like a broken voice
// rather than a broken install.
func binaryInstalled(root string) bool {
	info, err := os.Stat(binaryPath(root))
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return false
	}
	data, err := os.Stat(espeakData(binaryDir(root)))
	return err == nil && data.IsDir()
}

// voiceInstalled reports whether both halves of a voice are there and big
// enough to be real. A model is over 60 MB, so anything near the floor is a
// truncated transfer or an error page that arrived with a 200.
func voiceInstalled(voices, voice string) bool {
	model, err := os.Stat(modelPath(voices, voice))
	if err != nil || model.Size() < minModelBytes {
		return false
	}
	config, err := os.Stat(configPath(voices, voice))
	return err == nil && config.Size() >= minConfigBytes
}

// voicesInstalled reports whether a directory carries both voices. It is both
// or neither on purpose: the language is a setting someone flips, and a flip
// that starts a 60 MB download is a broken experience.
func voicesInstalled(voices string) bool {
	if strings.TrimSpace(voices) == "" {
		return false
	}
	for _, v := range allVoices {
		if !voiceInstalled(voices, v.Name) {
			return false
		}
	}
	return true
}

// installedVoices names the voices that are usable, for the setup check. The
// slice is never nil so the dashboard is handed [] rather than null.
func installedVoices(voices string) []string {
	names := make([]string, 0, len(allVoices))
	for _, v := range allVoices {
		if voiceInstalled(voices, v.Name) {
			names = append(names, v.Name)
		}
	}
	return names
}

// voiceFor picks the voice for the one language setting Socrates has.
//
// A tag like de-DE lands on the same voice as de. The setting is normalised
// elsewhere, but ending up in English because of a region suffix would be a
// wrong answer nobody gets told about.
func voiceFor(language string) string {
	code := strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if code == "de" {
		return VoiceGerman
	}
	return VoiceEnglish
}

// lengthScale turns a speaking rate into piper's --length_scale, which is the
// same idea upside down: the scale stretches every phoneme, so half the rate
// is twice the length. A rate outside a sane band is clamped rather than
// refused - the number comes from a slider, and speech that is a little too
// quick still beats an error where an answer should have been.
func lengthScale(rate float64) (string, bool) {
	if rate <= 0 {
		return "", false // "normal", which is what piper does with no flag at all
	}
	rate = math.Min(math.Max(rate, 0.5), 2)
	scale := math.Round(100/rate) / 100
	if math.Abs(scale-1) < 0.01 {
		return "", false
	}
	return strconv.FormatFloat(scale, 'f', -1, 64), true
}

// childEnv gives piper the environment it needs to find the libraries shipped
// beside it - onnxruntime, espeak-ng and the phonemizer - which live in the
// extracted directory and nowhere the dynamic loader would look by itself. It
// is set on the child only: putting an onnxruntime on the library path of the
// whole process would be an unpleasant surprise for anything else that ever
// links one. A piper from the PATH has no tree of ours and gets the
// environment untouched, because its libraries are wherever its packager put
// them.
//
// An existing value is rewritten rather than added to, because a second entry
// for the same variable is not merged by the loader, it is shadowed, and which
// of the two wins is not something to depend on.
func childEnv(tree string) []string {
	var keys []string
	switch {
	case tree == "":
	case runtime.GOOS == "darwin":
		keys = []string{"DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH"}
	case runtime.GOOS == "windows":
		// Windows looks for a DLL beside the executable that needs it before
		// it looks anywhere else, so piper.exe finds its own copies with no
		// help from here.
	default:
		keys = []string{"LD_LIBRARY_PATH"}
	}
	if len(keys) == 0 {
		return os.Environ()
	}

	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	previous := make(map[string]string, len(keys))
	environ := os.Environ()
	env := make([]string, 0, len(environ)+len(keys))
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if wanted[name] {
			previous[name] = value
			continue
		}
		env = append(env, entry)
	}
	for _, key := range keys {
		value := tree
		if previous[key] != "" {
			value += string(os.PathListSeparator) + previous[key]
		}
		env = append(env, key+"="+value)
	}
	return env
}

// capped collects what a process wrote without letting one that has gone wrong
// fill memory: the first few kilobytes say what happened and the rest is the
// same thing again. It reports the full length so the writer never sees a
// short write and starts complaining about that instead.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// flatten puts a process's complaint on one line, so it survives being put in
// a JSON error field and read back in a browser.
func flatten(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "it said nothing"
	}
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}
