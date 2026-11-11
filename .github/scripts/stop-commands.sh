#!/bin/sh
# Runs a command with GitHub's workflow commands stopped, and exits with its status (#115). Its output can carry
# third-party text (a registry's advisory titles and error bodies, a package author's deprecation note, a vulnerability
# database's summaries), and a line of it starting with "::" would otherwise be a command: a fake annotation, or an
# ::add-mask:: that hides a real finding. Its errors join its output, so nothing overtakes the stop.
#   sh .github/scripts/stop-commands.sh npm audit --omit=dev
set -u
token=$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')
echo "::stop-commands::$token"
status=0
"$@" 2>&1 || status=$?
# On a line of its own, even when the output didn't end with one.
echo
echo "::$token::"
exit "$status"
