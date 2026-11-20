package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/mudler/xlog"

	"github.com/mudler/LocalAI/pkg/grpc/base"
	"github.com/mudler/LocalAI/pkg/grpc/grpcerrors"
	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
	"github.com/mudler/LocalAI/pkg/httpclient"
)

// Radiance serves LocalAI requests by starting one radiance engine process and
// speaking its OpenAI-compatible API over HTTP.
//
// base.Base is embedded rather than base.SingleThread: radiance owns its
// scheduler (max-num-seqs, placement) and serves concurrent requests, so
// serialising every gRPC call would only throw that away.
type Radiance struct {
	base.Base

	// loadMu serialises Load and Free. Without it two concurrent loads each
	// start an engine and the loser is overwritten and never stopped, leaving
	// a process on the GPU.
	loadMu sync.Mutex

	mu      sync.Mutex
	eng     *engine
	cfgPath string

	ep     atomic.Pointer[endpoint]
	client *http.Client
}

func NewRadiance() *Radiance {
	// httpclient.New refuses redirects: the engine is on loopback and has no
	// reason to redirect, and following one would replay the server API key.
	// It also sets no body deadline, so long SSE streams are not cut short.
	return &Radiance{client: httpclient.New()}
}

func (r *Radiance) Load(opts *pb.ModelOptions) error {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()

	c, err := parseOptions(opts)
	if err != nil {
		return err
	}
	if c.port, err = c.choosePort(); err != nil {
		return err
	}
	config, err := c.render()
	if err != nil {
		return fmt.Errorf("radiance: build the engine config: %w", err)
	}
	cfgPath, err := writeEngineConfig(config)
	if err != nil {
		return err
	}
	if c.configOut != "" {
		// #nosec G306 G304 -- config_out is an operator-supplied debugging path, not request data
		if err := os.WriteFile(c.configOut, config, 0o600); err != nil {
			_ = os.Remove(cfgPath)
			return fmt.Errorf("radiance: write config_out %q: %w", c.configOut, err)
		}
	}

	baseURL := "http://" + net.JoinHostPort(probeHost(c.host), strconv.Itoa(c.port))
	// Stop any previous engine before the new one starts, so two engines never
	// own the same GPU at once.
	r.mu.Lock()
	r.releaseLocked()
	r.mu.Unlock()

	eng, err := startEngine(engineConfig{
		Path:         c.enginePath,
		ConfigPath:   cfgPath,
		Env:          envVars(opts),
		ReadyTimeout: c.readyTimeout,
		StopTimeout:  c.stopTimeout,
		BaseURL:      baseURL,
		Client:       r.client,
	})
	if err != nil {
		_ = os.Remove(cfgPath)
		return err
	}

	r.mu.Lock()
	r.eng, r.cfgPath = eng, cfgPath
	r.mu.Unlock()
	r.ep.Store(&endpoint{baseURL: baseURL, apiKey: c.apiKey, model: c.modelName, client: r.client})

	xlog.Info("radiance: engine ready", "model", c.modelName, "url", baseURL,
		"tp", c.tp, "max_model_len", c.maxModelLen, "max_num_seqs", c.maxNumSeqs)
	return nil
}

func envVars(opts *pb.ModelOptions) []string {
	vars := make([]string, 0, len(opts.GetEnvVars()))
	for k, v := range opts.GetEnvVars() {
		vars = append(vars, k+"="+v)
	}
	return vars
}

func writeEngineConfig(data []byte) (string, error) {
	f, err := os.CreateTemp("", "radiance-*.yaml")
	if err != nil {
		return "", fmt.Errorf("radiance: create the engine config: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("radiance: write the engine config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("radiance: write the engine config: %w", err)
	}
	return path, nil
}

func (r *Radiance) Free() error {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseLocked()
	return nil
}

func (r *Radiance) releaseLocked() {
	r.ep.Store(nil)
	if r.eng != nil {
		r.eng.close()
		r.eng = nil
	}
	if r.cfgPath != "" {
		_ = os.Remove(r.cfgPath)
		r.cfgPath = ""
	}
}

func (r *Radiance) Status() (pb.StatusResponse, error) {
	r.mu.Lock()
	eng := r.eng
	r.mu.Unlock()
	switch {
	case eng == nil:
		return pb.StatusResponse{State: pb.StatusResponse_UNINITIALIZED}, nil
	case !eng.alive():
		return pb.StatusResponse{State: pb.StatusResponse_ERROR}, nil
	}
	mem := &pb.MemoryUsageData{Breakdown: map[string]uint64{}}
	if pid := eng.pid(); pid > 0 {
		rss := processRSS(pid)
		mem.Breakdown["engine"] = rss
		mem.Total += rss
	}
	return pb.StatusResponse{State: pb.StatusResponse_READY, Memory: mem}, nil
}

// Embeddings is refused explicitly: radiance serves them only when the model
// has an embedding head, and this backend has no way to know that at load.
func (r *Radiance) Embeddings(*pb.PredictOptions) ([]float32, error) {
	return nil, errors.New("radiance: embeddings are not supported by this backend")
}

func (r *Radiance) endpoint() (*endpoint, error) {
	ep := r.ep.Load()
	if ep == nil {
		return nil, grpcerrors.ModelNotLoaded(backendName)
	}
	return ep, nil
}
