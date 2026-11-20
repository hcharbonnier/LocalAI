#!/bin/bash
set -e

CURDIR=$(dirname "$(realpath "$0")")

# The engine and its kernels/recipes ship inside this backend; the model
# options engine_path and RADIANCE_HOME still override them.
export RADIANCE_ENGINE_PATH="${RADIANCE_ENGINE_PATH:-$CURDIR/radiance-engine}"
export RADIANCE_HOME="${RADIANCE_HOME:-$CURDIR/share/radiance}"

exec "$CURDIR"/radiance "$@"
