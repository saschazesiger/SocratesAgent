package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/internet"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Every internet call is written into the process view before it happens and
// updated with its answer, exactly the way a shell command is. The point is
// that the person watching sees every request Socrates makes of the outside
// world, and can open it to read what came back.

func (e *Engine) execWebSearch(ctx context.Context, run *store.Run, raw string) string {
	var args struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `query`.")
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "The `query` argument was empty."
	}

	settings := e.Settings()
	if !settings.Internet.Enabled {
		return "Internet access is switched off in the admin dashboard, so there is no search to run."
	}

	step := e.addStep(run, "", store.StepSubTool, "web search",
		"Searched: "+firstLine(query, 120), store.StatusRunning, map[string]any{"query": query})

	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	out, err := internet.Search(ctx, settings.Internet, client, settings.OpenRouter.ChatModel, query, args.MaxResults)
	if err != nil {
		if ctx.Err() != nil {
			return e.finishInternetStep(step, "", "The run was stopped by the user.", true)
		}
		return e.finishInternetStep(step, "", "The search failed: "+err.Error(), true)
	}
	return e.finishInternetStep(step, out, out, false)
}

func (e *Engine) execWebFetch(ctx context.Context, run *store.Run, raw string) string {
	var args struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `url`.")
	}
	target := strings.TrimSpace(args.URL)
	if target == "" {
		return "The `url` argument was empty."
	}

	settings := e.Settings()
	if !settings.Internet.Enabled {
		return "Internet access is switched off in the admin dashboard, so pages cannot be fetched."
	}

	step := e.addStep(run, "", store.StepSubTool, "web fetch",
		"Read: "+firstLine(target, 160), store.StatusRunning, map[string]any{"url": target})

	out, err := internet.Fetch(ctx, settings.Internet, target, args.MaxChars)
	if err != nil {
		if ctx.Err() != nil {
			return e.finishInternetStep(step, "", "The run was stopped by the user.", true)
		}
		return e.finishInternetStep(step, "", "Could not read that page: "+err.Error(), true)
	}
	return e.finishInternetStep(step, out, out, false)
}

// finishInternetStep closes the step with what came back and hands the same
// text to the model, so the browser and the model always see the same thing.
func (e *Engine) finishInternetStep(step *store.Step, result, forModel string, failed bool) string {
	shown := result
	if shown == "" {
		shown = forModel
	}
	detail := map[string]any{"result": shown}
	if step.Detail != nil {
		var previous map[string]any
		if json.Unmarshal(step.Detail, &previous) == nil {
			previous["result"] = shown
			detail = previous
		}
	}
	step.Detail = mustJSON(detail)
	step.Status = store.StatusDone
	if failed {
		step.Status = store.StatusFailed
	}
	e.updateStep(step)
	return forModel
}
