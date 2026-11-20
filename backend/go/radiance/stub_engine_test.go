package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestMain doubles as the stub engine. When RADIANCE_STUB_MODE is set the test
// binary re-executes itself as the engine, so the engine lifecycle tests need
// neither a GPU nor the real radiance build.
//
// Modes:
//
//	ok     health 200; answers chat and completion requests
//	never  binds, but health always returns 503
//	exit   prints a load error on stderr and exits before binding
func TestMain(m *testing.M) {
	if mode := os.Getenv("RADIANCE_STUB_MODE"); mode != "" {
		runStub(mode)
		return
	}
	os.Exit(m.Run())
}

func runStub(mode string) {
	if mode == "exit" {
		_, _ = fmt.Fprintln(os.Stderr, "stub: cannot load model: no compatible GPU")
		os.Exit(2)
	}

	// Records which value of RADIANCE_STUB_PROBE the child actually received,
	// so a test can prove the model's environment overrides the inherited one.
	if p := os.Getenv("RADIANCE_STUB_ENV_LOG"); p != "" {
		_ = os.WriteFile(p, []byte(os.Getenv("RADIANCE_STUB_PROBE")), 0o600)
	}

	cfgPath := ""
	args := os.Args[1:]
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	var cfg struct {
		Server struct {
			Host string `yaml:"host"`
			Port int    `yaml:"port"`
		} `yaml:"server"`
	}
	raw, err := os.ReadFile(cfgPath) // #nosec G304 -- the parent passed this path
	if err != nil {
		fmt.Fprintf(os.Stderr, "stub: read config: %v\n", err)
		os.Exit(2)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "stub: parse config: %v\n", err)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	if mode == "never" {
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "loading", http.StatusServiceUnavailable)
		})
	} else {
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ready","model":"stub","in_flight":0,"queue_cap":4}`))
		})
		mux.HandleFunc("/v1/chat/completions", stubChat)
		mux.HandleFunc("/v1/completions", stubCompletion)
	}
	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "stub: serve: %v\n", err)
		os.Exit(2)
	}
}

func stubChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"stub answer"}}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, frame := range []string{
		`{"choices":[{"delta":{"content":"stub "}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":2}}`,
		"[DONE]",
	} {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func stubCompletion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"text":"stub completion"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
}

func testFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func stubEngineConfig(dir, mode string, readyTimeout time.Duration) engineConfig {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	port := testFreePort()
	cfgPath := filepath.Join(dir, "radiance.yaml")
	body := fmt.Sprintf("server:\n  host: 127.0.0.1\n  port: %d\n", port)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		panic(err)
	}
	return engineConfig{
		Path:         exe,
		ConfigPath:   cfgPath,
		Env:          []string{"RADIANCE_STUB_MODE=" + mode},
		ReadyTimeout: readyTimeout,
		StopTimeout:  2 * time.Second,
		BaseURL:      "http://127.0.0.1:" + strconv.Itoa(port),
	}
}
