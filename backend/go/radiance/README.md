# radiance

A LocalAI backend for the [Radiance](https://codeberg.org/StillDeadcode/radiance) inference engine
(Apache-2.0, with the upstream NOTICE). The backend is a pure-Go gRPC shim (`CGO_ENABLED=0`) that
starts the radiance engine as a child process with a generated YAML config and proxies LocalAI's
text requests to the engine's OpenAI-compatible HTTP API.

The shim is named `radiance` and the engine it launches is packaged next to it as `radiance-engine`.
The engine is built from the pinned upstream tag by the engine build unit, not by this package.

Status: Linux/amd64 with AMD ROCm (HIP) only. There is no CPU, CUDA, Darwin or Metal path.

## What the image contains

```
radiance                     the Go gRPC shim
radiance-engine              the engine (built from the pinned upstream tag)
run.sh                       entry point; sets RADIANCE_ENGINE_PATH and RADIANCE_HOME
share/radiance/              kernels and recipes (RADIANCE_HOME)
libexec/radiance/            engine helper programs, when the build produces any
lib/                         ROCm shared-library closure and ld.so, when present
LICENSE, NOTICE              upstream attribution
```

The engine is installed in the upstream layout (`bin/`, `libexec/radiance/`,
`share/radiance/{kernels,recipes}`). `run.sh` points `RADIANCE_HOME` at the packaged
`share/radiance`; `RADIANCE_KERNELS` and `OMP_NUM_THREADS` still override the engine's choices.

A `FROM scratch` image has no `/etc/ld.so.cache`, so when the image tree is extracted into a
backend directory its `lib/` holds the loader and the ROCm closure. `run.sh` puts `lib/` first on
`LD_LIBRARY_PATH` (the engine inherits it) and, when `lib/ld.so` exists, starts the shim through
that loader. A local install without `lib/` falls back to the host's loader and libraries.

## Options

Set these in the model config under `options:` as `key:value`. Values are strings; durations use Go
syntax (`900s`, `15m`).

| Option | Default | Meaning |
|---|---|---|
| `engine_path` | `$RADIANCE_ENGINE_PATH`, else `radiance-engine` next to the shim | Engine executable. |
| `host` | `127.0.0.1` | Address written to `server.host`. A wildcard address is probed over loopback. |
| `port` | `0` | Port written to `server.port`. `0` asks the shim to pick a free port before the engine starts. |
| `api_key` | `$RADIANCE_API_KEY` | Written as `server.api-key` when set. The shim sends it as a bearer token. |
| `cards` | `all` | `all`, or comma-separated card indexes (`0,1`), written as a YAML list. |
| `tp` | engine default | `tp` (tensor parallel). |
| `max_model_len` | engine default | `max-model-len`. |
| `max_num_seqs` | engine default | `max-num-seqs`. |
| `max_num_batched_tokens` | engine default | `max-num-batched-tokens`. |
| `kv_cache_dtype` | engine default | `kv-cache-dtype`. |
| `placement` | engine default | `placement`. |
| `served_model_name` | model name | `served-model-name`. |
| `override_chat_template` | engine default | `override-chat-template`. |
| `num_speculative_tokens` | engine default | `num-speculative-tokens`. |
| `gpu_headroom_mib` | engine default | `gpu-headroom-mib`, under `memory:`. |
| `ready_timeout` | `900s` | How long `Load` waits for `GET /health` to return 200. |
| `stop_timeout` | `15s` | How long the engine may take to exit after SIGTERM before it is killed. |
| `config_template` | none | Path to a Go `text/template` file. When set it replaces the generated YAML entirely. |
| `config_out` | none | Path where the generated (or rendered) YAML is also written, for debugging. |

The process environment is passed to the engine unchanged, plus every entry of the model config's
`environment` map (`opts.GetEnvVars()`); an entry whose name matches an inherited variable wins.
ROCm/HIP variables therefore reach the engine.

The generated file is:

```yaml
server:
  host: 127.0.0.1
  port: <chosen>
  api-key: <when set>
cards: all            # or [0, 1]
memory:
  gpu-headroom-mib: 512
models:
  <model name>:
    model: <path or repo id>
    tp: 2
    max-num-seqs: 4
    # ...
```

`config_template` receives `{{.Host}}`, `{{.Port}}`, `{{.Model}}`, `{{.ModelName}}` and
`{{.APIKey}}`. The shim still probes `{{.Host}}`/`{{.Port}}` for readiness, so the template must keep
the engine on the port it receives:

```yaml
server:
  host: {{.Host}}
  port: {{.Port}}
  api-key: {{.APIKey}}
models:
  {{.ModelName}}:
    model: {{.Model}}
    tp: 2
```

## Model files

`model` is resolved as follows:

- an absolute or models-path-relative `.rad`, `.gguf` or `.safetensors` file;
- a HuggingFace checkpoint directory containing `config.json` (or a model file);
- a HuggingFace repo id in `owner/name` form, which the engine downloads itself.

Anything else is refused at `Load`. This gate matters because LocalAI probes every installed
backend with a model that names no `backend:`; the radiance backend must not capture a llama.cpp
model by accepting arbitrary input.

The model key in `models:` is the config's model name, and requests are sent with that name. When
`served_model_name` is set, the engine also answers to it.

## Requests

`PredictRich`/`PredictStreamRich` (and their context variants) send:

- `POST /v1/chat/completions` when the request has structured messages;
- `POST /v1/completions` when it has only a rendered prompt.

Mapped fields: `max_tokens`, `temperature` (always sent, so 0 means greedy), `top_p`, `top_k`,
`min_p`, `seed`, `stop`, `frequency_penalty`, `presence_penalty`, `tools`, `tool_choice`, and
`response_format`.

`response_format` is taken from the request metadata when core passed one; otherwise a grammar
(`PredictOptions.Grammar`, which core fills for `json_object`/`json_schema`) is sent as
`{"type":"grammar","grammar":"..."}`.

Streaming uses the engine's SSE stream and stops at `data: [DONE]`. Content lands in
`Reply.message`, reasoning in `ChatDelta.reasoning_content`, and tool calls in
`ChatDelta.tool_calls` (index, id, name, argument chunks). With `stream_options.include_usage` the
final usage frame supplies `prompt_tokens`/`tokens`; usage attached to a content frame is reported
too. If the engine answers a streaming request with a single JSON body instead of SSE, that body is
emitted as one delta. A stream error frame is returned as
`Unavailable`; a non-2xx reply is mapped to a gRPC code (4xx `InvalidArgument`, 5xx `Unavailable`,
429 `ResourceExhausted`, 501 `Unimplemented`) with the engine body in the message.

A client that disconnects cancels the HTTP request to the engine (the context variants are what the
gRPC server calls).

## Readiness and lifetime

`Load` writes the config to a private temporary file, starts
`<engine_path> --config <file>`, and polls `GET /health` until it returns 200 or `ready_timeout`
expires. `503` means the engine is still loading and is retried. On timeout, or if the engine exits
first, the child is killed and the error includes the last 50 lines of its capped stdout/stderr.
The temporary config is removed when the load fails.

`Free` asks the engine to stop with SIGTERM, kills it after `stop_timeout`, and removes the
temporary config. On Linux the child gets `PDEATHSIG=SIGKILL` from a pinned OS thread, so a crashed
or SIGKILLed shim cannot leave the engine holding GPU memory.

`Status` reports `UNINITIALIZED` before a load, `ERROR` when the engine has exited, and `READY`
otherwise, with the engine process's resident memory under `engine` (VRAM is not reported; on a
discrete GPU the resident size excludes it).

The shim embeds `base.Base`, not `base.SingleThread`: radiance schedules requests itself
(`max-num-seqs`, placement), so serialising gRPC calls would defeat its scheduler.

## Not supported

- Embeddings (`/v1/embeddings` is 501 without an embedding head), tokenize/detokenize.
- Images, audio and video request inputs: this backend forwards text only.
- `logit_bias`, mirostat, typical-p and tail-free sampling (no radiance request field).
- `logprobs` (the engine's OpenAI surface used here has no matching request field).

## Tests

```
cd backend/go/radiance
CGO_ENABLED=0 go test ./...
```

The suite needs no GPU and no real engine. Readiness, early exit, missing engine and the full
load/predict/free path run against the test binary re-executed as a stub engine; request mapping,
SSE parsing, tool calls, reasoning, usage and error mapping run against `httptest` fakes.

## Packaging

`make package` runs `package.sh`, which expects the engine binary staged as `radiance-engine` next
to the shim (or `RADIANCE_ENGINE_BIN`), the `share/radiance` and `libexec/radiance` trees (or
`RADIANCE_SHARE_DIR` / `RADIANCE_LIBEXEC_DIR`), the ROCm library closure in `lib/` (or
`RADIANCE_LIB_DIR`), and an upstream `LICENSE` and `NOTICE` (next to `package.sh` or under
`sources/radiance/`). It copies all of them into `package/`.
