// fakemodel serves the scripted Responses API of internal/agent/codex/fakemodel
// on a local port, so that the real `codex app-server` can run without a model
// or an account: `fakemodel -addr 127.0.0.1:18080 -log requests.jsonl`, and
// a CODEX_HOME whose config.toml is fakemodel.Config.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/saschazesiger/SocratesAgent/internal/agent/codex/fakemodel"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "address to listen on")
	logPath := flag.String("log", "", "file every request is appended to")
	flag.Parse()
	var w io.Writer
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	log.Fatal(http.ListenAndServe(*addr, fakemodel.New(w)))
}
