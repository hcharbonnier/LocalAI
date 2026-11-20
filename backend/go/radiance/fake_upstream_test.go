package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/gomega"

	"github.com/mudler/LocalAI/pkg/httpclient"
)

type recordedRequest struct {
	Method string
	Path   string
	Auth   string
	JSON   map[string]any
}

// scriptedResponse is the reply for one path. SSE, when set, is written as
// "data: <frame>" events and wins over Body.
type scriptedResponse struct {
	Status int
	Body   string
	SSE    []string
}

type fakeUpstream struct {
	*httptest.Server

	mu        sync.Mutex
	requests  []recordedRequest
	responses map[string]scriptedResponse
}

func newFakeUpstream() *fakeUpstream {
	f := &fakeUpstream{responses: map[string]scriptedResponse{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeUpstream) script(path string, r scriptedResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[path] = r
}

func (f *fakeUpstream) replyJSON(path string, body any) {
	raw, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	f.script(path, scriptedResponse{Status: http.StatusOK, Body: string(raw)})
}

func (f *fakeUpstream) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeUpstream) last() recordedRequest {
	reqs := f.recorded()
	ExpectWithOffset(1, reqs).NotTo(BeEmpty(), "upstream received no request")
	return reqs[len(reqs)-1]
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recordedRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rec.JSON)
	}

	f.mu.Lock()
	f.requests = append(f.requests, rec)
	resp, ok := f.responses[r.URL.Path]
	f.mu.Unlock()

	if !ok {
		http.Error(w, "no scripted response for "+r.URL.Path, http.StatusNotFound)
		return
	}
	if resp.SSE != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range resp.SSE {
			_, _ = io.WriteString(w, "data: "+frame+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = io.WriteString(w, resp.Body)
}

// testBackend points a Radiance backend at a fake engine without starting a
// process.
func testBackend(url string) *Radiance {
	r := NewRadiance()
	r.ep.Store(&endpoint{baseURL: url, model: "stub-model", client: httpclient.New()})
	return r
}

func sseJSON(v any) string {
	raw, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}
