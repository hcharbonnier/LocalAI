+++
disableToc = false
title = "Radiance backend"
weight = 15
devbuild = "feature"
aliases = ["/features/radiance/"]
+++

[Radiance](https://codeberg.org/StillDeadcode/radiance) is a C++20/HIP inference engine for
AMD GPUs, published under the Apache License 2.0. LocalAI runs it with the `radiance` backend:
the backend writes a Radiance YAML configuration, starts `radiance --config <file>` as a child
process, and proxies the OpenAI-compatible API that the engine serves.

{{% callout style="note" %}}
Radiance runs on Linux x86-64 (amd64) with an AMD GPU under ROCm. It has no CUDA, Metal, Intel
or CPU path, and no Windows or macOS build.
{{% /callout %}}

## What you need

- A Linux x86-64 host with an AMD GPU from [the hardware table](#supported-hardware).
- The `amdgpu` kernel driver and the `/dev/kfd` and `/dev/dri` device nodes. The image carries
  the ROCm user-space libraries, so a separate ROCm install on the host isn't required.
- A model in a form the engine reads (see [Models](#models)).
- Enough VRAM for the weights, the KV cache and the activations. The `gpu_headroom_mib` option
  keeps a margin free.

## Supported hardware

The image carries kernels for three GPU families:

| Kernel library | GPUs |
|---|---|
| `gfx1201` | RDNA4: Radeon RX 9000 series, Radeon AI PRO R9700 |
| `gfx11-generic` | RDNA3: Radeon RX 7000 series, built as `gfx11-generic` |
| `gfx1151` | Strix Halo (Ryzen AI Max) |

Any other architecture has no kernel in the image. That includes NVIDIA GPUs, Intel GPUs,
Apple Silicon, older AMD families and CPU-only hosts.

## Installing

```bash
local-ai backends install radiance
```

The backend installs as `radiance` from the default backend gallery, so there's no gallery to set
up. It declares the capability `amd: rocm-radiance`, so LocalAI selects it only on a host that has
a supported AMD GPU.

| Image tag | Build |
|---|---|
| `quay.io/go-skynet/local-ai-backends:latest-gpu-rocm-hipblas-radiance` | Release build |
| `quay.io/go-skynet/local-ai-backends:master-gpu-rocm-hipblas-radiance` | Development build from master |

The development image is hidden in the Backends page until you turn on **Development builds**
under Filters. Both images carry an engine built from the Radiance source tag `v2.2.0`, because
upstream publishes no binaries.

## Running LocalAI in a container

When LocalAI itself runs in a container, pass the AMD devices through. The engine opens the
KFD and render nodes directly, and ROCm needs the elevated limits:

```bash
docker run --device /dev/kfd --device /dev/dri \
  --group-add video --group-add render \
  --security-opt seccomp=unconfined \
  --ulimit memlock=-1 \
  -p 8080:8080 \
  localai/localai:latest-gpu-hipblas
```

`/dev/kfd` is the compute interface, `/dev/dri` holds the render nodes, the `video` and `render`
groups grant access to them, `seccomp=unconfined` lets ROCm make the system calls it needs, and an
unlimited `memlock` limit keeps the engine from failing to pin memory. A native install needs the
same device nodes and driver, without the container flags.

LocalAI unpacks the backend image and runs its `run.sh` in the LocalAI container, so the engine
sees the devices you passed to that container.

## Models

Radiance reads three model forms, plus GGUF for a few architectures:

| Form | What it is |
|---|---|
| `.rad` container | A single file produced by `rad-convert` from a Hugging Face checkpoint. |
| Hugging Face checkpoint directory | The safetensors weights, `config.json` and tokenizer files, copied to the model directory. |
| Hugging Face repository id | The engine downloads the checkpoint on first load. The host needs network access and disk space. |
| GGUF file | Supported for a few architectures only. Check that the model's own documentation names Radiance support before you rely on it. |

`rad-convert` ships with the Radiance source tree at tag `v2.2.0`. Run it on the checkpoint once,
on the host or in a container, and copy the resulting `.rad` file under the LocalAI models path.
Prefer a `.rad` file or a Hugging Face checkpoint over GGUF unless the model's own documentation
names GGUF support.

There are no gallery entries for Radiance yet. Write the model YAML by hand and place the weights
where `parameters.model` points. The backend doesn't convert weights and it doesn't download a
`.rad` file: `rad-convert` and the checkpoint download are steps you run.

## Model configuration

A complete model file:

```yaml
name: my-radiance-model
backend: radiance
parameters:
  model: my-model.rad
options:
  - cards:0,1
  - tp:2
  - max_model_len:32768
  - max_num_seqs:8
  - max_num_batched_tokens:8192
  - served_model_name:my-model
  - gpu_headroom_mib:1024
  - ready_timeout:900s
  - stop_timeout:15s
  - config_out:/tmp/my-model.radiance.yaml
```

The `options:` list holds `key:value` strings. `parameters.model` is the `.rad` file, the
checkpoint directory or the repository id. A relative file or directory resolves under the LocalAI
models path. A repository id is passed through to the engine, which downloads it.

Don't set a LocalAI chat template on the model. The engine applies the model's own chat template
and reports it on `GET /props`. Use `override_chat_template` only when you want a different one.

### Options

| Option | Default | What it does |
|---|---|---|
| `engine_path` | `$RADIANCE_ENGINE_PATH`, else `<backend dir>/radiance-engine` | Path to the `radiance` engine binary. Set it to run a binary you built yourself. |
| `host` | `127.0.0.1` | Address the engine's HTTP server binds to. The backend talks to it on this address. |
| `port` | `0` | Port for the engine. `0` asks the system for a free port. |
| `api_key` | none | Key the backend uses with the engine. |
| `cards` | `all` | GPUs the engine uses: `all`, or a comma-separated list of indices such as `0,1`. |
| `tp` | engine default | Tensor parallel size. |
| `max_model_len` | engine default | Context length in tokens. |
| `max_num_seqs` | engine default | Maximum sequences scheduled at the same time. |
| `max_num_batched_tokens` | engine default | Token budget of one prefill batch. |
| `kv_cache_dtype` | engine default | Data type of the KV cache. |
| `placement` | engine default | Placement strategy passed to the engine. |
| `served_model_name` | engine default | Model name the engine reports. |
| `override_chat_template` | none | Chat template that replaces the model's own. |
| `num_speculative_tokens` | engine default | Number of tokens proposed by speculative decoding. |
| `gpu_headroom_mib` | engine default | VRAM in MiB kept free as headroom. |
| `ready_timeout` | `900s` | How long the backend waits for the engine to become ready before it fails the load. |
| `stop_timeout` | `15s` | How long the backend waits for the engine to exit before it ends the process. |
| `config_template` | none | Path to a Radiance YAML file with Go `text/template` placeholders. It replaces the configuration the backend generates. |
| `config_out` | none | Path where the backend also writes the generated YAML, for debugging. |

A `config_template` uses these placeholders: `{{.Host}}`, `{{.Port}}`, `{{.Model}}`,
`{{.ModelName}}` and `{{.APIKey}}`. The backend renders them, writes the result and starts the
engine with it. Use it when the engine needs a key the options above don't set. A template
replaces the generated file: the engine gets only what the template renders, and the placeholders
carry the values the backend resolved.

`config_out` writes the YAML the backend generated. Keep it when you debug a startup failure, then
compare it with the options and the template.

The backend passes the model's `env:` entries and its own process environment to the engine, so
ROCm and HIP variables such as `HIP_VISIBLE_DEVICES` and `HSA_*` reach the engine. Use the map
form:

```yaml
env:
  HIP_VISIBLE_DEVICES: "0,1"
```

## Capabilities

The engine serves an OpenAI-compatible API and LocalAI exposes the same routes in front of it.

| Capability | Status |
|---|---|
| Chat completions, `/v1/chat/completions` | yes |
| Completions, `/v1/completions` | yes |
| Embeddings, `/v1/embeddings` | Answers `501` unless the model has an embedding head |
| Responses, `/v1/responses` | yes |
| Anthropic messages, `/v1/messages` | yes |
| Media endpoints | Served by the engine |
| Streaming | Server-sent events, ending with `data: [DONE]` |
| Tools and function calling | yes |
| Reasoning | Returned in `reasoning_content` |
| `response_format` | `json_object` and `json_schema` |
| Chat template | Applied by Radiance and reported on `GET /props` |
| Health | `GET /health`: `200` when ready, `503` while loading, stuck or stopping |

Readiness is the engine's own answer. At `200` the model is loaded and serving; at `503` the
backend hasn't started proxying requests yet.

## Troubleshooting

| What you see | What to do |
|---|---|
| `failed to load engine plugin`, or a missing symbol when the engine starts | A local engine binary at `engine_path` and the Radiance libraries that ship with the backend image come from different versions, so the engine and its plugin disagree on the ABI. Use one version for both: remove `engine_path` to use the bundled engine, or build the engine and the plugin from the same tag. |
| The engine exits with `no kernel image is available` or a message about an unsupported `gfx` target | The GPU architecture isn't in the image. Check the `gfx` value that `rocminfo` reports and compare it with [the hardware table](#supported-hardware). There's no CPU fallback. |
| `GET /health` stays at `503` with state `loading` | The engine is reading weights. On a slow disk wait, and raise `ready_timeout` in the model YAML when the model is large. |
| `GET /health` reports `503` with state `stuck` | The engine stopped making progress. Stop the model and read the backend logs. Lower `max_model_len`, `max_num_seqs` or the number of `cards` before you retry. |
| `GET /health` reports `503` with state `stopping` | The engine is shutting down. Start the model again after `stop_timeout`. |
| The model is rejected at startup and the engine exits | The engine doesn't support the architecture, the quantization or the file layout. Read the engine's last lines in the backend logs. Convert a full Hugging Face checkpoint with `rad-convert` and point `parameters.model` at the `.rad` file. |
| `connection refused` right after the load starts | The engine crashed before it opened its port. Read the backend logs for the reason. |

A load that fails leaves the model unloaded. The engine's error stays in the backend logs, so fix
the cause before you send more traffic.

## Limitations

- Linux x86-64 and AMD ROCm only. There is no CUDA, Metal, Intel, CPU, Windows or macOS support,
  so it isn't selected on an NVIDIA, Intel or Apple host.
- Upstream publishes no release binaries. The backend image carries an engine built from the
  source tag, and the engine version is pinned to `v2.2.0` until the backend moves to a newer
  tag. A model that needs a newer upstream feature won't load.
- No gallery models yet. Model files and the model YAML are manual setup.
- GGUF support covers only a few architectures.
- Embeddings are available only for models that carry an embedding head.
