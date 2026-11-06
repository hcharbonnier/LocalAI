# ds4 backend Makefile.
#
# Upstream pin lives below as DS4_VERSION?=5379c1c9ac315f1540371b06a3db88491d4059ff
# (.github/bump_deps.sh) can find and update it - matches the
# llama-cpp / ik-llama-cpp / turboquant convention.

DS4_VERSION?=5379c1c9ac315f1540371b06a3db88491d4059ff
DS4_REPO?=https://github.com/antirez/ds4

CURRENT_MAKEFILE_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
BUILD_DIR := build

BUILD_TYPE ?=
# package.sh sources scripts/build/package-gpu-libs.sh, which dispatches on
# BUILD_TYPE. Export it so a bare `make package` run outside Docker packages
# the right GPU runtime closure too.
export BUILD_TYPE
NATIVE ?= false
JOBS ?= $(shell nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)

UNAME_S := $(shell uname -s)

CMAKE_ARGS ?= -DCMAKE_BUILD_TYPE=Release

# nvcc must be told the target architecture explicitly for a cublas build, and
# this is not a tuning knob. Upstream's Makefile leaves CUDA_ARCH empty and its
# `cuda` target REFUSES to build without one, offering `cuda-spark`
# (CUDA_ARCH=sm_121) and `cuda-generic` (CUDA_ARCH=native) instead. We drive its
# object targets directly, which bypasses that guard: nvcc then compiles with no
# -arch at all, and the kernels run as JIT'd PTX for its default architecture.
# On GB10 (sm_121) that silently produced corrupt inference output above a
# ~128-token prefill batch and ~77x slower prefill (4.21 t/s vs 325.70 t/s,
# measured on the same box with the same model). No CI runner has a GPU, so
# `native` has nothing to enumerate there.
#
# Upstream's CUDA_ARCH takes a SINGLE value (see its sm_120/sm_121 special cases
# and the `-arch=$(CUDA_ARCH)` fallback), so it cannot express the fat binary
# these images need. NVCC_ARCH_FLAGS is overridden instead: a command-line
# assignment wins over the `:=` in upstream's Makefile, and its NVCCFLAGS
# expands whatever we pass.
#
# The architecture lists are copied from backend/go/vllm-cpp/Makefile rather
# than invented, so the two CUDA images cover the same GPUs: amd64 datacenter +
# consumer, and l4t/arm64 covering Orin (87), Thor (110) and GB10 (121a).
#
# -DDS4_CUDA_HAVE_MXF4=1 is deliberately NOT set. Upstream only defines it for
# single-arch sm_120/sm_121 builds and guards the code with a plain #ifdef
# rather than __CUDA_ARCH__, so it cannot be combined with older archs in one
# fat binary. It gates an optional MXFP4 indexer fast path whose #ifndef branch
# returns 0 and falls back to the generic path, so omitting it costs some speed
# on GB10, not correctness. Revisit if upstream adds __CUDA_ARCH__ guards.
#
# An EMPTY CUDA_MAJOR_VERSION means a local developer build, not CI: fall back
# to upstream's own `native` handling, which needs a GPU present but is what a
# developer building on their own machine wants. Both variables are `?=` so an
# explicit value on the command line always wins.
UNAME_M := $(shell uname -m)
CUDA_MAJOR_VERSION ?=
ifeq ($(BUILD_TYPE),cublas)
ifeq ($(CUDA_MAJOR_VERSION),13)
ifeq ($(UNAME_M),aarch64)
    DS4_NVCC_ARCH_FLAGS ?= -gencode arch=compute_87,code=sm_87 \
                           -gencode arch=compute_90a,code=sm_90a \
                           -gencode arch=compute_100a,code=sm_100a \
                           -gencode arch=compute_110,code=sm_110 \
                           -gencode arch=compute_121a,code=sm_121a
else
    DS4_NVCC_ARCH_FLAGS ?= -gencode arch=compute_80,code=sm_80 \
                           -gencode arch=compute_86,code=sm_86 \
                           -gencode arch=compute_89,code=sm_89 \
                           -gencode arch=compute_90a,code=sm_90a \
                           -gencode arch=compute_100a,code=sm_100a \
                           -gencode arch=compute_103a,code=sm_103a \
                           -gencode arch=compute_120a,code=sm_120a \
                           -gencode arch=compute_121a,code=sm_121a
endif
    DS4_ARCH_MAKEVARS := NVCC_ARCH_FLAGS="$(DS4_NVCC_ARCH_FLAGS)"
else ifeq ($(CUDA_MAJOR_VERSION),)
    # Local build: let upstream resolve the host GPU.
    DS4_ARCH_MAKEVARS := CUDA_ARCH=native
else
    $(error CUDA_MAJOR_VERSION=$(CUDA_MAJOR_VERSION) has no architecture list here (13 does). Leave it empty for a native build, or pass DS4_NVCC_ARCH_FLAGS explicitly.)
endif
endif

# ROCm (BUILD_TYPE=hipblas) drives upstream's strix-halo objects. Upstream's
# ROCM_CFLAGS hardcodes a single --offload-arch=$(ROCM_ARCH), so restate the
# flag set with the project-wide AMDGPU_TARGETS fat list (the same targets as
# llama-cpp / audio-cpp / the CI workflows). ROCM_MMQ_FLAGS is :=-derived from
# ROCM_CFLAGS upstream, so a command-line ROCM_CFLAGS= covers the mmq objects
# too. Host TUs need upstream's strix-halo CFLAGS restated: -DDS4_ROCM_BUILD
# selects the ROCm engine in ds4.c (the Linux default is CUDA). Every ROCm TU
# (host C and hipcc .cu) also needs -fPIC: upstream links with hipcc, while
# LocalAI links these prebuilt objects with g++/CMake, whose Ubuntu default is
# PIE. Without it the link fails with R_X86_64_32 relocations in .bss. gfx1030
# is excluded because rocWMMA has no RDNA2 support.
comma := ,
DS4_DEFAULT_AMDGPU_TARGETS := gfx908,gfx90a,gfx942,gfx950,gfx1100,gfx1101,gfx1102,gfx1151,gfx1200,gfx1201
AMDGPU_TARGETS ?= $(DS4_DEFAULT_AMDGPU_TARGETS)
ifeq ($(strip $(AMDGPU_TARGETS)),)
    # A command-line `AMDGPU_TARGETS=` (the Dockerfile always forwards it, empty
    # by default) outranks a plain assignment; override it so the fat default
    # still applies.
    override AMDGPU_TARGETS := $(DS4_DEFAULT_AMDGPU_TARGETS)
endif
DS4_OFFLOAD_FLAGS := $(foreach a,$(subst $(comma), ,$(AMDGPU_TARGETS)),--offload-arch=$(a))
DS4_ROCM_CFLAGS := -O3 -ffast-math -g -fno-finite-math-only -fPIC -pthread \
    -D__HIP_PLATFORM_AMD__ -Wno-unused-command-line-argument $(DS4_OFFLOAD_FLAGS)
DS4_ROCM_MAKEVARS := ROCM_CFLAGS="$(DS4_ROCM_CFLAGS)"
DS4_ROCM_HOST_CFLAGS := -O3 -ffast-math -g -Wall -Wextra -std=c99 \
    -D_GNU_SOURCE -fno-finite-math-only -fPIC -DDS4_ROCM_BUILD
ifeq ($(NATIVE),true)
    DS4_ROCM_HOST_CFLAGS += -march=native
endif

# Upstream splits image preprocessing, distributed inference, tensor-parallel
# transport, the SSD expert cache, layer placement, and engram lookup into
# GPU-agnostic translation units. They are shared by every GPU mode, so append
# them unconditionally below.
ifeq ($(BUILD_TYPE),cublas)
    CMAKE_ARGS += -DDS4_GPU=cuda
    DS4_OBJ_TARGET := ds4.o ds4_image.o ds4_cuda.o ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o ds4_engram.o \
        cuda/mmq/ds4_ggml_stubs.o cuda/mmq/ds4_mmq.o cuda/mmq/ds4_mmq_d2r.o \
        cuda/mmq/quantize.o cuda/mmq/mmid.o cuda/mmq/mmvq.o cuda/mmq/ds4_repack.o
else ifeq ($(BUILD_TYPE),hipblas)
    CMAKE_ARGS += -DDS4_GPU=rocm
    DS4_OBJ_TARGET := ds4.o ds4_image.o ds4_rocm.o ds4_rocm_compat.o ds4_rocm_unavailable.o \
        ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o ds4_engram.o \
        cuda/mmq/ds4_ggml_stubs.rocm.o cuda/mmq/ds4_mmq.rocm.o cuda/mmq/quantize.rocm.o \
        cuda/mmq/mmid.rocm.o cuda/mmq/mmvq.rocm.o cuda/mmq/d2r_stubs.rocm.o
else ifeq ($(UNAME_S),Darwin)
    CMAKE_ARGS += -DDS4_GPU=metal
    DS4_OBJ_TARGET := ds4.o ds4_image.o ds4_metal.o ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o ds4_engram.o
else
    # CPU reference path (Linux only - macOS CPU path is broken by VM bug per ds4 README).
    CMAKE_ARGS += -DDS4_GPU=cpu
    DS4_OBJ_TARGET := ds4_cpu.o ds4_image.o ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o
endif

ifneq ($(NATIVE),true)
    CMAKE_ARGS += -DDS4_NATIVE=OFF
endif

.PHONY: grpc-server package clean purge test all
all: grpc-server

# Clone the upstream ds4 source at the pinned commit. Directory acts as the
# target so make only re-clones when missing. After a DS4_VERSION bump,
# run 'make purge && make' to refetch (or rely on CI's clean build).
ds4:
	mkdir -p ds4
	cd ds4 && \
	git init -q && \
	git remote add origin $(DS4_REPO) && \
	git fetch --depth 1 origin $(DS4_VERSION) && \
	git checkout FETCH_HEAD

# Local patches carry the ROCm fixes ds4 needs on this project's ROCm 7.2.1
# base (device-only rsqrtf, gfx11-only WMMA builtins). Apply them to the
# pristine clone before any object is built. The reverse check makes the rule
# idempotent if the stamp is ever lost while the tree still has the patches.
DS4_PATCHES := $(sort $(wildcard $(CURRENT_MAKEFILE_DIR)patches/*.patch))
ds4/.localai-patched: ds4 $(DS4_PATCHES)
	cd ds4 && for p in $(DS4_PATCHES); do \
		if git apply --reverse --check "$$p" >/dev/null 2>&1; then \
			echo "ds4: $$p already applied"; \
		else \
			git apply "$$p" && echo "ds4: applied $$p"; \
		fi; \
	done
	touch $@

# Build ds4's engine object files via its own Makefile, which already encodes
# the right per-platform compile flags (Objective-C/Metal on Darwin, nvcc on Linux+CUDA).
ds4/ds4.o: ds4/.localai-patched
ifeq ($(BUILD_TYPE),cublas)
	+$(MAKE) -C ds4 $(DS4_ARCH_MAKEVARS) $(DS4_OBJ_TARGET)
else ifeq ($(BUILD_TYPE),hipblas)
	+$(MAKE) -C ds4 $(DS4_ROCM_MAKEVARS) CFLAGS="$(DS4_ROCM_HOST_CFLAGS)" $(DS4_OBJ_TARGET)
else ifeq ($(UNAME_S),Darwin)
	+$(MAKE) -C ds4 ds4.o ds4_image.o ds4_metal.o ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o ds4_engram.o
else
	+$(MAKE) -C ds4 ds4_cpu.o ds4_image.o ds4_distributed.o ds4_tp.o ds4_ssd.o ds4_layer_pack.o
endif

grpc-server: ds4/ds4.o
	mkdir -p $(BUILD_DIR)
	cd $(BUILD_DIR) && cmake $(CMAKE_ARGS) $(CURRENT_MAKEFILE_DIR) && cmake --build . --config Release -j $(JOBS)
	cp $(BUILD_DIR)/grpc-server grpc-server
	cp $(BUILD_DIR)/ds4-worker ds4-worker

package: grpc-server
	bash package.sh

test:
	@echo "ds4 backend: e2e coverage at tests/e2e-backends/ (BACKEND_BINARY mode)"

clean:
	rm -rf $(BUILD_DIR) grpc-server ds4-worker package
	if [ -d ds4 ]; then $(MAKE) -C ds4 clean; fi

purge: clean
	rm -rf ds4
