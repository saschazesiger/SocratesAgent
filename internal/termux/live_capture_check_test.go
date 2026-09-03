package termux

// TestLiveCaptureCheck is a manual harness for feeding real tmux
// capture-pane output through scrapeScreen and codexSource.Read, used only
// during the regression check of the OpenCode permission-event fix against
// the real codex binary. It is not part of the CI suite: it does nothing
// unless SOCRATES_LIVE_CAPTURE is set, and it must not be committed.
//
// Set SOCRATES_LIVE_CAPTURE to a file with two lines:
//
//	line 1: the tmux pane title (#{pane_title})
//	line 2: the rest is the captured screen (tmux capture-pane -p -J -S -40)
//
// and SOCRATES_LIVE_KIND to "codex" or "shell".
import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

func TestLiveCaptureCheck(t *testing.T) {
	path := os.Getenv("SOCRATES_LIVE_CAPTURE")
	if path == "" {
		t.Skip("SOCRATES_LIVE_CAPTURE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	parts := strings.SplitN(string(raw), "\n", 2)
	title := parts[0]
	screen := ""
	if len(parts) > 1 {
		screen = parts[1]
	}

	kind := harnesses.KindCodex
	if os.Getenv("SOCRATES_LIVE_KIND") == "shell" {
		kind = harnesses.KindShell
	}

	// codexSource.Read only looks at the title, so drive it directly rather
	// than standing up a whole snapshot.
	titleObs := codexSource{}.Read(nil, snapshot{pane: paneLine{title: title}})
	screenObs := scrapeScreen(kind, screen)

	fmt.Printf("LIVECHECK kind=%s title=%q titleObs=%+v screenObs=%+v\n",
		kind, title, titleObs, screenObs)
}
