#!/bin/bash
set -e

CURDIR=$(dirname "$(realpath "$0")")

# The engine and its kernels/recipes ship inside this backend; the model
# options engine_path and RADIANCE_HOME still override them.
export RADIANCE_ENGINE_PATH="${RADIANCE_ENGINE_PATH:-$CURDIR/radiance-engine}"
export RADIANCE_HOME="${RADIANCE_HOME:-$CURDIR/share/radiance}"
export LD_LIBRARY_PATH="$CURDIR/lib:${LD_LIBRARY_PATH:-}"

if [ -f "$CURDIR/lib/ld.so" ]; then
	exec "$CURDIR/lib/ld.so" "$CURDIR/radiance" "$@"
fi

exec "$CURDIR/radiance" "$@"
