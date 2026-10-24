package agent

// FakeHarness is the name the scripted agent is registered under. It and
// FakeEnv are here rather than beside the scripted agent, which needs a Unix
// child process, so that code naming it builds on every platform.
const FakeHarness = "fake"

// FakeEnv switches the scripted agent on: it is registered only in a process
// whose environment has FakeEnv=1, so a production server never offers it.
const FakeEnv = "SOCRATES_FAKE_AGENT"

// FakeKeyEnv is where an API-key account's key reaches the scripted agent's
// CLI, the way SOCRATES_CLAUDE_API_KEY reaches Claude Code's adapter.
const FakeKeyEnv = "SOCRATES_FAKE_API_KEY"
