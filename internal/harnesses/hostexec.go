package harnesses

import "sync"

// ------------------------------------------------------------ on the host

// onHostPath is where the image keeps the wrapper that runs one command on the
// host machine. It is the agents' counterpart of the host shell: a Shell
// session becomes a login shell over there, while an agent stays in the
// container - its binary, its credentials and its generated files are in the
// data volume, which the host does not have - and reaches across one command
// at a time.
const onHostPath = "/usr/local/bin/on-host"

// onHostFile is the instruction document for the harness that takes its
// instructions as a file rather than as text, one per session.
const onHostFile = "on-host.md"

// hostExecAvailable reports whether the wrapper is shipped and the host is
// reachable, which is the same two questions the host shell asks. It is a
// variable so that the tests can answer it for a machine that is neither.
var hostExecAvailable = sync.OnceValue(func() bool {
	return findHostShell(onHostPath, hostReachableOnce) != ""
})

// hostReachableOnce is hostReachable asked once per process, whichever of the
// host shell and on-host asks first: the answer is a property of how the
// container was started, and each probe is a process.
var hostReachableOnce = sync.OnceValue(hostReachable)

// onHostInstructions is what an agent is told about the host, or "" when there
// is nothing to tell. Without it the wrapper would be a file on PATH that no
// agent knows the purpose of, and a question about the machine would be
// answered about the container it happens to run in.
//
// It is appended to each agent's own instructions and never replaces them, and
// it says what the command can do as plainly as the command does it: every
// session runs with its approvals bypassed, so this is root on the host with
// nobody asked first.
func onHostInstructions() string {
	if !hostExecAvailable() {
		return ""
	}
	return `# The host machine

You run inside a container. Use ` + "`on-host`" + ` for anything on the host machine it
runs on - the Kubernetes cluster (kubectl, helm, k3s), system services,
packages, files outside the mounted directories:

- ` + "`on-host kubectl get pods -A`" + ` runs one command on the host.
- ` + "`on-host -c 'journalctl -u k3s | tail -50'`" + ` runs a shell script there. Use
  this whenever pipes or redirections must act on the host; without -c they
  run in the container.

It runs as root, with no confirmation. Look before changing anything, make the
smallest change the task needs, and never delete, restart or reconfigure what
the task does not call for.
`
}
