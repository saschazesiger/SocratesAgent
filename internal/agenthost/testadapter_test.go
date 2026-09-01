package agenthost

import (
	"github.com/saschazesiger/SocratesAgent/internal/agenthost/hosttest"
)

// The scripted adapter lives in hosttest because internal/engine needs the
// same one: it tests the run lifecycle against a real host process, and a
// second copy of an adapter is a second thing to keep in step. Importing it
// here is what registers it under the id "test" in this test binary.
type step = hosttest.Step

const testScriptEnv = hosttest.ScriptEnv
