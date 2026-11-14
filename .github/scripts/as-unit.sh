#!/bin/sh
# Runs a command as root with exactly the capabilities a systemd unit gives its service, read from the unit file
# itself (its one CapabilityBoundingSet= line, and NoNewPrivileges=), so a test and the unit's capabilities can't drift
# (#138). The unit's other sandboxing (system calls, namespaces, paths) isn't modelled.
# systemd starts a root service with nothing inheritable, so on exec its permitted and effective sets are the bounding
# set; setpriv does the same, and this checks that it did before it runs the command.
#
# Usage: sudo sh as-unit.sh UNIT COMMAND [ARG...]
set -eu
die() {
	echo "as-unit.sh: $*" >&2
	exit 2
}
[ $# -ge 2 ] || die "usage: as-unit.sh UNIT COMMAND [ARG...]"
unit=$1
shift
[ "$(id -u)" = 0 ] || die "run as root"
lines=$(grep -c '^CapabilityBoundingSet=' "$unit" || true)
[ "$lines" = 1 ] || die "want one CapabilityBoundingSet= line in $unit, found $lines"
caps=$(sed -n 's/^CapabilityBoundingSet=//p' "$unit")
case $caps in
'' | *'~'*) die "an empty or inverted CapabilityBoundingSet= in $unit isn't handled" ;;
esac
# shellcheck disable=SC2086 # the unit's list, split on its spaces
names=$(printf '%s\n' $caps | tr 'A-Z' 'a-z' | sed 's/^cap_//')
want=$(printf '%s\n' "$names" | sort | paste -sd, -)
keep=$(printf '%s\n' "$names" | sed 's/^/+/' | paste -sd, -)
nnp=
if grep -Eiq '^NoNewPrivileges=[[:space:]]*(yes|true|on|1)[[:space:]]*$' "$unit"; then
	nnp=--no-new-privs
fi

# shellcheck disable=SC2086 # $nnp is one flag or none
got=$(setpriv --inh-caps=-all --ambient-caps=-all --bounding-set=-all,"$keep" $nnp -- setpriv -d |
	sed -n 's/^Capability bounding set: //p' | tr , '\n' | sort | paste -sd, -)
[ "$got" = "$want" ] || die "the bounding set came out as $got, want $want"
# shellcheck disable=SC2086
setpriv --inh-caps=-all --ambient-caps=-all --bounding-set=-all,"$keep" $nnp -- sh -c '
	set -- $(awk "/^Cap(Prm|Eff|Bnd):/ {print \$2}" /proc/self/status)
	[ "$1" = "$3" ] && [ "$2" = "$3" ]' || die "permitted and effective aren't the bounding set"
echo "as-unit.sh: as root with only $want (from $unit)${nnp:+, no new privileges}"
# shellcheck disable=SC2086
exec setpriv --inh-caps=-all --ambient-caps=-all --bounding-set=-all,"$keep" $nnp -- "$@"
