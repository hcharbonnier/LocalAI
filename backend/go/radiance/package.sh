#!/bin/bash
#
# Assemble the backend image contents in package/:
#   radiance          the Go gRPC shim (pure Go, no shared libraries)
#   radiance-engine   the engine built from the pinned upstream tag
#   run.sh            entry point; points the shim at the engine and its data
#   share/radiance/   kernels and recipes (RADIANCE_HOME)
#   libexec/radiance/ engine helper programs, when the build produces any
#   lib/              ROCm shared-library closure and ld.so, when staged
#   LICENSE, NOTICE   upstream Apache-2.0 attribution
#
# The engine build unit stages its output here before this script runs. Every
# path can be overridden for a different staging layout.

set -e

CURDIR=$(dirname "$(realpath "$0")")

ENGINE_BIN=${RADIANCE_ENGINE_BIN:-$CURDIR/radiance-engine}
SHARE_DIR=${RADIANCE_SHARE_DIR:-$CURDIR/share/radiance}
LIBEXEC_DIR=${RADIANCE_LIBEXEC_DIR:-$CURDIR/libexec/radiance}
LIB_DIR=${RADIANCE_LIB_DIR:-$CURDIR/lib}

if [ ! -x "$CURDIR/radiance" ]; then
	echo "ERROR: $CURDIR/radiance is missing, run 'make radiance' first" >&2
	exit 1
fi
if [ ! -x "$ENGINE_BIN" ]; then
	echo "ERROR: $ENGINE_BIN is missing, build and stage the radiance engine first" >&2
	exit 1
fi

find_license() {
	local name=$1
	for p in "$CURDIR/$name" "$CURDIR/sources/radiance/$name" "$CURDIR/upstream/$name"; do
		if [ -f "$p" ]; then
			echo "$p"
			return 0
		fi
	done
	return 1
}

LICENSE=$(find_license LICENSE) || {
	echo "ERROR: no LICENSE found (set up sources/radiance/ or place LICENSE next to package.sh)" >&2
	exit 1
}
NOTICE=$(find_license NOTICE) || {
	echo "ERROR: no NOTICE found; Apache-2.0 redistribution requires it" >&2
	exit 1
}

rm -rf "$CURDIR/package"
mkdir -p "$CURDIR/package"

cp -avf "$CURDIR/radiance" "$CURDIR/package/"
cp -avf "$ENGINE_BIN" "$CURDIR/package/radiance-engine"
cp -avf "$CURDIR/run.sh" "$CURDIR/package/"
cp -avf "$LICENSE" "$CURDIR/package/LICENSE"
cp -avf "$NOTICE" "$CURDIR/package/NOTICE"
chmod +x "$CURDIR/package/run.sh" "$CURDIR/package/radiance" "$CURDIR/package/radiance-engine"

if [ -d "$SHARE_DIR" ]; then
	mkdir -p "$CURDIR/package/share"
	cp -af "$SHARE_DIR" "$CURDIR/package/share/radiance"
else
	echo "WARNING: $SHARE_DIR is missing; the engine will fall back to its compiled-in RADIANCE_HOME" >&2
	mkdir -p "$CURDIR/package/share/radiance"
fi

if [ -d "$LIBEXEC_DIR" ]; then
	mkdir -p "$CURDIR/package/libexec"
	cp -af "$LIBEXEC_DIR" "$CURDIR/package/libexec/radiance"
fi

if [ -d "$LIB_DIR" ]; then
	cp -af "$LIB_DIR" "$CURDIR/package/lib"
else
	echo "WARNING: $LIB_DIR is missing; the extracted tree will rely on the host's ROCm libraries" >&2
	mkdir -p "$CURDIR/package/lib"
fi

echo "Packaging completed successfully"
ls -liah "$CURDIR/package/"
