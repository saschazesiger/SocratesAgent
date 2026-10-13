#!/bin/sh
# A login shell on the host machine, for a pane that runs in the container.
#
# Socrates starts this as "<binary> -l" and the arguments are not used: the
# login shell is started on the other side of the namespaces, where the profile
# it reads actually lives. `--version` is the one argument answered, because
# that is how the admin dashboard probes a binary.
#
# Nothing here decides whether the jump is wanted. internal/harnesses/shell.go
# names this file only when it is possible at all - the container has to have
# been started with --pid=host and the right to enter namespaces - so a normal
# container and a host installation never reach it.
set -e

host_home=${SOCRATES_HOST_HOME:-/root}

case ${1-} in
--version) exec nsenter -t 1 -m /bin/bash --version ;;
esac

# Both of these are asked on the other side, because the answer here would be
# about the container. A working directory is the same directory on both sides
# when it is there because of a bind mount of the same name, and has nowhere to
# land when it exists only in the image or in the data volume.
dir=$PWD
nsenter -t 1 -m /bin/sh -c '[ -d "$1" ]' sh "$dir" 2>/dev/null || dir=$host_home
shell=/bin/bash
nsenter -t 1 -m /bin/sh -c '[ -x /bin/bash ]' 2>/dev/null || shell=/bin/sh

# HOME in the image is inside the data volume, which the host does not have: a
# login shell would look for its profile in a directory that is not there.
exec nsenter -t 1 -m -u -i -n -p --wdns="$dir" \
	env HOME="$host_home" TERM="$TERM" "$shell" -l
