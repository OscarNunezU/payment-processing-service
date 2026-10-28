#!/bin/sh
# Checks the authorization model with TLC. The protocol as designed must hold,
# and each mutation, which switches off one safeguard, must be caught: if a
# mutation passes, that safeguard is not needed or the model cannot see the
# mistake, and both are worth knowing.
#
#   spec/check.sh /absolute/path/to/tla2tools.jar
set -eu

jar=${1:?usage: spec/check.sh /absolute/path/to/tla2tools.jar}
cd "$(dirname "$0")/authorization"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

tlc() {
	java -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers auto \
		-metadir "$work/states" -cleanup -config "$1" Authorization.tla
}

for cfg in Safety Liveness; do
	echo "== The protocol as designed: $cfg"
	if ! tlc "$cfg.cfg" >"$work/out" 2>&1; then
		cat "$work/out"
		if grep -q -E 'is violated|properties were violated' "$work/out"; then
			echo "FAIL $cfg: the protocol as designed breaks a property"
		else
			echo "FAIL $cfg: TLC stopped for another reason"
		fi
		exit 1
	fi
	grep -E 'distinct states found|No error has been found' "$work/out"
done

echo "== Mutations, one safeguard off each"
status=0
for cfg in mutations/*.cfg; do
	name=$(basename "$cfg" .cfg)
	if tlc "$cfg" >"$work/out" 2>&1; then
		echo "FAIL $name: no counterexample"
		status=1
	elif grep -q 'is violated' "$work/out"; then
		echo "ok   $name: $(grep -o 'Invariant [A-Za-z]* is violated' "$work/out" | head -1)"
	else
		cat "$work/out"
		echo "FAIL $name: TLC stopped for another reason"
		status=1
	fi
done
exit "$status"
