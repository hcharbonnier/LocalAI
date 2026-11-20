package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
)

const (
	backendName = "radiance"

	// engineBinary is shipped next to the Go binary by package.sh and named in
	// the generated config. The upstream program is installed as `radiance`;
	// the LocalAI backend keeps its own name and launches the engine as a
	// child, so the two binaries need distinct file names.
	engineBinary = "radiance-engine"

	envEnginePath = "RADIANCE_ENGINE_PATH"
	envAPIKey     = "RADIANCE_API_KEY"

	defaultHost         = "127.0.0.1"
	defaultReadyTimeout = 900 * time.Second
	defaultStopTimeout  = 15 * time.Second
)

// options is the parsed form of the model options. Every value that ends up in
// the generated YAML is here; the engine only ever sees the file.
type options struct {
	enginePath string
	host       string
	port       int
	apiKey     string

	cards any

	tp                   int
	maxModelLen          int
	maxNumSeqs           int
	maxNumBatchedTokens  int
	kvCacheDtype         string
	placement            string
	servedModelName      string
	overrideChatTemplate string
	numSpeculativeTokens int
	gpuHeadroomMiB       int

	readyTimeout time.Duration
	stopTimeout  time.Duration

	configTemplate string
	configOut      string

	model     string
	modelName string
}

// radianceYAML mirrors the subset of radiance.yaml this backend can configure.
// The upstream file also accepts scheduler, defaults and groups; those have no
// option here, and their upstream defaults apply.
type radianceYAML struct {
	Server serverYAML           `yaml:"server"`
	Cards  any                  `yaml:"cards,omitempty"`
	Memory *memoryYAML          `yaml:"memory,omitempty"`
	Models map[string]modelYAML `yaml:"models"`
}

type serverYAML struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api-key,omitempty"`
}

type memoryYAML struct {
	GPUHeadroomMiB int `yaml:"gpu-headroom-mib"`
}

type modelYAML struct {
	Model                string `yaml:"model"`
	TP                   int    `yaml:"tp,omitempty"`
	MaxNumSeqs           int    `yaml:"max-num-seqs,omitempty"`
	MaxModelLen          int    `yaml:"max-model-len,omitempty"`
	MaxNumBatchedTokens  int    `yaml:"max-num-batched-tokens,omitempty"`
	KVCacheDtype         string `yaml:"kv-cache-dtype,omitempty"`
	Placement            string `yaml:"placement,omitempty"`
	ServedModelName      string `yaml:"served-model-name,omitempty"`
	OverrideChatTemplate string `yaml:"override-chat-template,omitempty"`
	NumSpeculativeTokens int    `yaml:"num-speculative-tokens,omitempty"`
}

// templateData is the vocabulary of the config_template option. The names are
// part of the option contract; renaming a field breaks user templates.
type templateData struct {
	Host      string
	Port      int
	Model     string
	ModelName string
	APIKey    string
}

func parseOptions(opts *pb.ModelOptions) (*options, error) {
	kv := map[string]string{}
	for _, o := range opts.GetOptions() {
		k, v, _ := strings.Cut(o, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" {
			kv[k] = v
		}
	}

	c := &options{
		host:         defaultHost,
		cards:        "all",
		readyTimeout: defaultReadyTimeout,
		stopTimeout:  defaultStopTimeout,
	}

	model, name, err := locateModel(opts)
	if err != nil {
		return nil, err
	}
	c.model, c.modelName = model, name

	c.enginePath = firstNonEmpty(kv["engine_path"], os.Getenv(envEnginePath))
	if c.enginePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("radiance: cannot locate the backend executable to find %s: %w", engineBinary, err)
		}
		c.enginePath = filepath.Join(filepath.Dir(exe), engineBinary)
	}

	if v := kv["host"]; v != "" {
		c.host = v
	}
	if c.port, err = optionalInt(kv, "port", 0, 65535); err != nil {
		return nil, err
	}
	c.apiKey = firstNonEmpty(kv["api_key"], os.Getenv(envAPIKey))
	if c.cards, err = parseCards(kv["cards"]); err != nil {
		return nil, err
	}

	for _, n := range []struct {
		key string
		dst *int
	}{
		{"tp", &c.tp},
		{"max_model_len", &c.maxModelLen},
		{"max_num_seqs", &c.maxNumSeqs},
		{"max_num_batched_tokens", &c.maxNumBatchedTokens},
		{"num_speculative_tokens", &c.numSpeculativeTokens},
		{"gpu_headroom_mib", &c.gpuHeadroomMiB},
	} {
		if *n.dst, err = optionalInt(kv, n.key, 1, 1<<31-1); err != nil {
			return nil, err
		}
	}

	c.kvCacheDtype = kv["kv_cache_dtype"]
	c.placement = kv["placement"]
	c.servedModelName = kv["served_model_name"]
	c.overrideChatTemplate = kv["override_chat_template"]
	c.configTemplate = kv["config_template"]
	c.configOut = kv["config_out"]

	if c.readyTimeout, err = optionalDuration(kv, "ready_timeout", defaultReadyTimeout); err != nil {
		return nil, err
	}
	if c.stopTimeout, err = optionalDuration(kv, "stop_timeout", defaultStopTimeout); err != nil {
		return nil, err
	}
	return c, nil
}

// locateModel resolves the artifact radiance is asked to serve and refuses
// anything that is not one of its forms. Without this gate, LocalAI's greedy
// backend probing (a model config with no explicit backend) would bind an
// arbitrary model to this backend.
func locateModel(opts *pb.ModelOptions) (artifact, name string, err error) {
	name = strings.TrimSpace(opts.GetModel())
	artifact = strings.TrimSpace(opts.GetModelFile())
	if artifact == "" {
		artifact = name
	}
	if artifact == "" {
		return "", "", errors.New("radiance: the model config names no model")
	}
	if p, ok := findModelPath(artifact, opts.GetModelPath()); ok {
		artifact = p
	} else if pathExists(artifact) {
		return "", "", fmt.Errorf("radiance: %q is not a .rad/.gguf/.safetensors file or an HF checkpoint directory", artifact)
	} else if !isRepoID(artifact) {
		return "", "", fmt.Errorf("radiance: %q is neither a .rad/.gguf/.safetensors file, an HF checkpoint directory, nor an HF repo id (owner/name)", artifact)
	}
	if name == "" {
		name = filepath.Base(artifact)
	}
	return artifact, name, nil
}

func findModelPath(p, modelsPath string) (string, bool) {
	cands := []string{p}
	if !filepath.IsAbs(p) && modelsPath != "" {
		cands = append(cands, filepath.Join(modelsPath, p))
	}
	for _, c := range cands {
		st, err := os.Stat(c)
		if err != nil {
			continue
		}
		if st.IsDir() {
			if isCheckpointDir(c) {
				return c, true
			}
			continue
		}
		if isModelFile(c) {
			return c, true
		}
	}
	return "", false
}

func isCheckpointDir(dir string) bool {
	if fileExists(filepath.Join(dir, "config.json")) {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && isModelFile(e.Name()) {
			return true
		}
	}
	return false
}

func isModelFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".rad", ".gguf", ".safetensors":
		return true
	}
	return false
}

// isRepoID matches HuggingFace's owner/name form. A bare name is rejected:
// radiance would try to download it, and so would any other model.
func isRepoID(p string) bool {
	if filepath.IsAbs(p) || strings.Contains(p, "\\") || strings.Contains(p, ":") || strings.ContainsAny(p, " \t") {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func parseCards(v string) (any, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "all" {
		return "all", nil
	}
	var out []int
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("radiance: bad cards entry %q: expected all or comma-separated card indexes", f)
		}
		out = append(out, n)
	}
	return out, nil
}

func optionalInt(kv map[string]string, key string, lo, hi int) (int, error) {
	v, ok := kv[key]
	if !ok || strings.TrimSpace(v) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("radiance: bad %s %q", key, v)
	}
	return n, nil
}

func optionalDuration(kv map[string]string, key string, def time.Duration) (time.Duration, error) {
	v, ok := kv[key]
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("radiance: bad %s %q", key, v)
	}
	return d, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// render produces the YAML the engine is started with, or the rendered
// config_template when one is set.
func (c *options) render() ([]byte, error) {
	if c.configTemplate != "" {
		return c.renderTemplate()
	}
	cfg := radianceYAML{
		Server: serverYAML{Host: c.host, Port: c.port, APIKey: c.apiKey},
		Cards:  c.cards,
		Models: map[string]modelYAML{
			c.modelName: {
				Model:                c.model,
				TP:                   c.tp,
				MaxNumSeqs:           c.maxNumSeqs,
				MaxModelLen:          c.maxModelLen,
				MaxNumBatchedTokens:  c.maxNumBatchedTokens,
				KVCacheDtype:         c.kvCacheDtype,
				Placement:            c.placement,
				ServedModelName:      c.servedModelName,
				OverrideChatTemplate: c.overrideChatTemplate,
				NumSpeculativeTokens: c.numSpeculativeTokens,
			},
		},
	}
	if c.gpuHeadroomMiB > 0 {
		cfg.Memory = &memoryYAML{GPUHeadroomMiB: c.gpuHeadroomMiB}
	}
	return yaml.Marshal(cfg)
}

func (c *options) renderTemplate() ([]byte, error) {
	// #nosec G304 -- config_template is an operator-supplied model option, not request data
	raw, err := os.ReadFile(filepath.Clean(c.configTemplate))
	if err != nil {
		return nil, fmt.Errorf("radiance: read config_template: %w", err)
	}
	t, err := template.New(filepath.Base(c.configTemplate)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("radiance: parse config_template: %w", err)
	}
	var buf bytes.Buffer
	data := templateData{Host: c.host, Port: c.port, Model: c.model, ModelName: c.modelName, APIKey: c.apiKey}
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("radiance: render config_template: %w", err)
	}
	return buf.Bytes(), nil
}

// choosePort returns the configured port, or a free one when the option left
// it at 0. Radiance binds explicitly from the config, so the port must be
// picked before the engine starts.
func (c *options) choosePort() (int, error) {
	if c.port != 0 {
		return c.port, nil
	}
	l, err := net.Listen("tcp", net.JoinHostPort(probeHost(c.host), "0"))
	if err != nil {
		return 0, fmt.Errorf("radiance: choose a free port: %w", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// probeHost is the address the shim uses to reach the engine. A wildcard bind
// address is reachable over loopback.
func probeHost(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::", "[::]":
		return "::1"
	}
	return host
}
