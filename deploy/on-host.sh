#!/bin/sh
# One command on the host machine, for an agent whose pane runs in the
# container.
#
#   on-host kubectl get pods -A        the arguments are the command, as given
#   on-host -c 'kubectl get pods | wc -l'   a script for the host's shell
#   on-host <<'EOF' ... EOF            no arguments: the script is read from stdin
#
# The command runs in every namespace of the host's PID 1 - the host's files,
# network, processes and hostname - as root, in a login shell, so that the
# profile that puts kubectl on PATH or sets KUBECONFIG is read. Its exit status
# is the command's.
#
# The environment is the host's, not the pane's: the pane carries HOME inside
# the data volume, IS_SANDBOX, and the agents' own variables, none of which mean
# anything over there. TERM and the locale are the exceptions, because they
# describe the terminal the output goes to.
#
# internal/harnesses tells the agents about this file only when it can be used
# - the container has to have been started with --pid=host and the right to
# enter namespaces - which is the same test the host shell passes.
set -e

host_home=${SOCRATES_HOST_HOME:-/root}
unset script

case ${1-} in
-h | --help)
	sed -n '2,13s/^# \{0,1\}//p' "$0"
	exit 0
	;;
-c)
	[ $# -eq 2 ] || { echo "usage: on-host -c 'script'" >&2; exit 2; }
	script=$2
	set --
	;;
--)
	shift
	;;
esac

# The working directory is the same one on both sides only when it is there
# because of a bind mount of the same name; otherwise the command starts in the
# host's home. This is asked on the other side, because the answer here would be
# about the container.
dir=$PWD
nsenter -t 1 -m /bin/sh -c '[ -d "$1" ]' sh "$dir" 2>/dev/null || dir=$host_home
shell=/bin/bash
nsenter -t 1 -m /bin/sh -c '[ -x /bin/bash ]' 2>/dev/null || shell=/bin/sh

# A login shell reads the profile, then `exec "$@"` hands the process over to
# the command, so signals and the exit status are the command's own. A -c
# script is run by that login shell itself, and with no command at all it reads
# its script from stdin.
if [ -n "${script+set}" ]; then
	set -- "$shell" -l -c "$script"
elif [ $# -eq 0 ]; then
	set -- "$shell" -l -s
else
	set -- "$shell" -l -c 'exec "$@"' on-host "$@"
fi

exec nsenter -t 1 -m -u -i -n -p --wdns="$dir" \
	env -i HOME="$host_home" USER=root LOGNAME=root \
	PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
	TERM="${TERM:-dumb}" LANG="${LANG:-C.UTF-8}" \
	"$@"
