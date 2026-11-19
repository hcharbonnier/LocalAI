#!/bin/bash

set -e


## Patches

## Apply patches from the `patches` directory. Runs under set -e so a
## rejected patch aborts the build here, loudly, instead of surfacing later
## as a confusing compile error. A missing or empty patches dir is a no-op.
if [ -d "patches" ]; then
    for patch in $(ls patches); do
        echo "Applying patch $patch"
        # Dry-run first so a patch that targets newer upstream sources is
        # skipped as a whole instead of leaving a half-applied tree behind.
        # Forks that lack the patched feature are handled by the task-type
        # probes further down (they mark the gRPC server accordingly).
        if patch -d llama.cpp/ -p1 --dry-run --forward < patches/$patch; then
            patch -d llama.cpp/ -p1 --forward < patches/$patch
        else
            echo "==> patch $patch does not apply to this llama.cpp revision, skipping"
        fi
    done
fi

## Apple RDMA link fixup.

## ggml-rpc hands Apple's librdma to the linker with
## target_link_options(ggml-rpc PRIVATE "LINKER:-weak_library,..."). Link options are not
## a usage requirement of a static library, so in our BUILD_SHARED_LIBS=OFF build the flag
## dies with libggml-rpc.a and every ibv_* symbol transport-apple.cpp reaches for comes out
## undefined when grpc-server and ggml-rpc-server link. Re-declare the same weak link as
## INTERFACE so it travels to whoever links the static library.
##
## Guarded on the marker so a second prepare.sh over the same checkout is a no-op, and on
## GGML_RPC_RDMA_APPLE so forks that branched before the Apple RDMA transport (turboquant,
## bonsai) are left alone.
RPC_CMAKE=llama.cpp/ggml/src/ggml-rpc/CMakeLists.txt
if [ -f "$RPC_CMAKE" ] && grep -q "GGML_RPC_RDMA_APPLE" "$RPC_CMAKE" && ! grep -q "LOCALAI_RDMA_IFACE" "$RPC_CMAKE"; then
    echo "==> ggml-rpc carries the Apple RDMA transport, re-declaring its weak librdma link as INTERFACE"
    cat >> "$RPC_CMAKE" <<'EOF'

# LOCALAI_RDMA_IFACE: added by backend/cpp/llama-cpp/prepare.sh
if (GGML_RPC_RDMA AND APPLE AND NOT BUILD_SHARED_LIBS)
    target_link_options(ggml-rpc INTERFACE "LINKER:-weak_library,${RDMA_LIB}")
endif()
EOF
fi

for file in $(ls llama.cpp/tools/server/); do
    cp -rfv llama.cpp/tools/server/$file llama.cpp/tools/grpc-server/
done

cp -r CMakeLists.txt llama.cpp/tools/grpc-server/
cp -r grpc-server.cpp llama.cpp/tools/grpc-server/
cp -r decision_compat.h llama.cpp/tools/grpc-server/
cp -r decision_images.h llama.cpp/tools/grpc-server/
# Model-load diagnostics (included by grpc-server.cpp) and their standalone
# regression test.
cp -r model_load_error.h llama.cpp/tools/grpc-server/
cp -r model_load_error_test.cpp llama.cpp/tools/grpc-server/
# Shared message-reconstruction helpers (included by grpc-server.cpp) and their
# unit test (compiled only when -DLLAMA_GRPC_BUILD_TESTS=ON).
cp -r message_content.h llama.cpp/tools/grpc-server/
cp -r message_content_test.cpp llama.cpp/tools/grpc-server/
# Generic passthrough parser staging and its standalone regression test.
cp -r passthrough_options.h llama.cpp/tools/grpc-server/
cp -r passthrough_options_test.cpp llama.cpp/tools/grpc-server/
# TTS request validation (included by grpc-server.cpp) and its standalone
# regression test.
cp -r tts_request_options.h llama.cpp/tools/grpc-server/
cp -r tts_request_options_test.cpp llama.cpp/tools/grpc-server/
# Thread-count default normalization and its standalone regression test.
cp -r thread_params.h llama.cpp/tools/grpc-server/
cp -r thread_params_test.cpp llama.cpp/tools/grpc-server/
# Slot-count resolution (option over LLAMACPP_PARALLEL) and its standalone
# regression test.
cp -r parallel_params.h llama.cpp/tools/grpc-server/
cp -r parallel_params_test.cpp llama.cpp/tools/grpc-server/
# moe_cache_mib option parsing and its standalone regression test.
cp -r moe_cache_option.h llama.cpp/tools/grpc-server/
cp -r moe_cache_option_test.cpp llama.cpp/tools/grpc-server/
# Parent-death watcher (included by grpc-server.cpp) and its standalone unit
# test (run via backend/cpp/run-unit-tests.sh; also buildable under ctest).
cp -r parent_watch.h llama.cpp/tools/grpc-server/
cp -r parent_watch_test.cpp llama.cpp/tools/grpc-server/
# Dead-stream tracker (included by grpc-server.cpp) and its standalone unit
# test (run via backend/cpp/run-unit-tests.sh; also buildable under ctest).
cp -r stream_peer.h llama.cpp/tools/grpc-server/
cp -r stream_peer_test.cpp llama.cpp/tools/grpc-server/
# Slot cache save/restore helpers (included by grpc-server.cpp) and their
# unit test (compiled only when -DLLAMA_GRPC_BUILD_TESTS=ON).
cp -r slot_cache.h llama.cpp/tools/grpc-server/
cp -r slot_cache_test.cpp llama.cpp/tools/grpc-server/
cp -rfv llama.cpp/vendor/nlohmann/json.hpp llama.cpp/tools/grpc-server/
cp -rfv llama.cpp/vendor/cpp-httplib/httplib.h llama.cpp/tools/grpc-server/

## Fork-skew probe. Upstream folded common_params::use_mmap / use_mlock /
## use_direct_io into a single `load_mode` enum (ggml-org/llama.cpp#20834).
## turboquant and bonsai compile this very same grpc-server.cpp against forks
## that branched before that change, so the field set is decided from the
## checkout in front of us rather than from a per-fork build flag: the flavor
## targets disagree on whether they forward CMAKE_ARGS or EXTRA_CMAKE_ARGS, and
## probing heals itself the moment a fork rebases past the refactor.
if grep -q "LLAMA_LOAD_MODE_MMAP" llama.cpp/include/llama.h; then
    echo "==> llama.cpp carries the load-mode enum, using common_params::load_mode"
    LEGACY_LOAD_MODE=0
else
    echo "==> llama.cpp predates the load-mode enum, using the legacy mmap/mlock/direct-io booleans"
    LEGACY_LOAD_MODE=1
fi
if grep -q "server_metrics metrics;" llama.cpp/tools/server/server-task.h; then
    HAS_SERVER_METRICS=1
else
    HAS_SERVER_METRICS=0
fi
if grep -q "mtmd_helper_init_opt" llama.cpp/tools/mtmd/mtmd-helper.h; then
    HAS_MTMD_INIT_OPT=1
else
    HAS_MTMD_INIT_OPT=0
fi
# Rerank gained separate query/document media buffers in newer trees.
if grep -q "query_files" llama.cpp/tools/server/server-common.h 2>/dev/null; then
    RERANK_HAS_FILES=1
else
    RERANK_HAS_FILES=0
fi
if grep -q "llm_add_n_cpu_ffn_overrides" llama.cpp/common/common.h; then
    HAS_N_CPU_FFN_HELPER=1
else
    HAS_N_CPU_FFN_HELPER=0
fi
# MoE expert GPU cache (--moe-cache-mib). Forks that branched earlier lack the field.
if grep -q "moe_cache_size" llama.cpp/common/common.h; then
    HAS_MOE_CACHE=1
else
    HAS_MOE_CACHE=0
fi
# On-demand tensor reading (--lazy-mode). Forks that branched earlier lack the field.
if grep -q "llama_lazy_mode lazy_mode" llama.cpp/common/common.h; then
    HAS_LAZY_MODE=1
else
    HAS_LAZY_MODE=0
fi
# Newer servers return template state separately from the request JSON.
if grep -q "common_chat_session & out_session" llama.cpp/tools/server/server-common.h; then
    HAS_CHAT_SESSION=1
else
    HAS_CHAT_SESSION=0
fi
# The server-schema split kept the per-slot n_ctx argument until upstream
# dropped it. Forks that predate the drop still expect it.
if grep -q "n_ctx_slot" llama.cpp/tools/server/server-schema.h 2>/dev/null; then
    SERVER_SCHEMA_HAS_N_CTX=1
else
    SERVER_SCHEMA_HAS_N_CTX=0
fi
# common/json.h (with common_json_error) was introduced after some forks
# branched; those trees raise nlohmann exceptions from json::parse directly.
if grep -q "common_json_error" llama.cpp/common/json.h 2>/dev/null; then
    HAS_COMMON_JSON_ERROR=1
else
    HAS_COMMON_JSON_ERROR=0
fi
cat > llama.cpp/tools/grpc-server/llama_compat.h <<EOF
// Generated by backend/cpp/llama-cpp/prepare.sh. Do not edit.
#pragma once
#define LOCALAI_LEGACY_LOAD_MODE ${LEGACY_LOAD_MODE}
#define LOCALAI_HAS_CHAT_SESSION ${HAS_CHAT_SESSION}
#define LOCALAI_HAS_SERVER_METRICS ${HAS_SERVER_METRICS}
#define LOCALAI_HAS_MTMD_INIT_OPT ${HAS_MTMD_INIT_OPT}
#define LOCALAI_HAS_N_CPU_FFN_HELPER ${HAS_N_CPU_FFN_HELPER}
#define LOCALAI_HAS_MOE_CACHE ${HAS_MOE_CACHE}
#define LOCALAI_HAS_LAZY_MODE ${HAS_LAZY_MODE}
#define LOCALAI_HAS_COMMON_JSON_ERROR ${HAS_COMMON_JSON_ERROR}
#define LOCALAI_SERVER_SCHEMA_HAS_N_CTX ${SERVER_SCHEMA_HAS_N_CTX}
#define LOCALAI_RERANK_HAS_FILES ${RERANK_HAS_FILES}
EOF

## Fork-skew for optional server task types. The Score task comes from the
## patches above; TTS was added to upstream after some forks branched. When the
## checkout lacks the task type, mark the copied gRPC server so those RPCs
## answer UNIMPLEMENTED instead of referencing enums that do not exist.
if ! grep -q "SERVER_TASK_TYPE_SCORE" llama.cpp/tools/server/server-task.h; then
    echo "==> llama.cpp has no Score task, disabling the LocalAI score RPC"
    bash disable-score-task.sh llama.cpp/tools/grpc-server/grpc-server.cpp
fi
if ! grep -q "SERVER_TASK_TYPE_TTS" llama.cpp/tools/server/server-task.h; then
    echo "==> llama.cpp has no TTS task, disabling the LocalAI TTS RPCs"
    bash disable-tts-task.sh llama.cpp/tools/grpc-server/grpc-server.cpp
fi

set +e
if grep -q "grpc-server" llama.cpp/tools/CMakeLists.txt; then
    echo "grpc-server already added"
else
    echo "add_subdirectory(grpc-server)" >> llama.cpp/tools/CMakeLists.txt
fi
set -e
