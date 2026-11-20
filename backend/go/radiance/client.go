package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mudler/xlog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxErrorBody caps the engine body quoted in an error. gRPC status messages
// stay small while still carrying radiance's JSON error text, which failover
// scans for request problems such as context overflows.
const maxErrorBody = 500

// endpoint is the part of the loaded backend needed to talk to the engine.
type endpoint struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func (ep *endpoint) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ep.baseURL, "/")+path, body)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "radiance: build %s request: %v", path, err)
	}
	if ep.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.apiKey)
	}
	return req, nil
}

func (r *Radiance) postJSON(ctx context.Context, ep *endpoint, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "radiance: encode %s request: %v", path, err)
	}
	req, err := ep.newRequest(ctx, http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ep.client.Do(req)
	if err != nil {
		return transportError(path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(path, resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return transportError(path, ctxErr)
		}
		return status.Errorf(codes.Internal, "radiance: decode %s response: %v", path, err)
	}
	return nil
}

func (r *Radiance) postStream(ctx context.Context, ep *endpoint, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "radiance: encode %s request: %v", path, err)
	}
	req, err := ep.newRequest(ctx, http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := ep.client.Do(req)
	if err != nil {
		return nil, transportError(path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, statusError(path, resp)
	}
	return resp, nil
}

// transportError maps a failed round trip to a gRPC status. A dead engine is
// Unavailable so failover moves to another target.
func transportError(path string, err error) error {
	code := codes.Unavailable
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	}
	xlog.Warn("radiance: engine request failed", "path", path, "error", err)
	return status.Errorf(code, "radiance: engine %s: %v", path, err)
}

// statusError maps a non-2xx reply. 5xx is the engine failing (Unavailable,
// so failover retries); 4xx is the request (InvalidArgument). 429 is a capacity
// signal and 501 a capability gap, matching how core treats them elsewhere.
func statusError(path string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
	msg := strings.TrimSpace(string(raw))
	if len(msg) > maxErrorBody {
		msg = msg[:maxErrorBody] + "..."
	}
	msg = strings.ToValidUTF8(msg, "")

	var code codes.Code
	switch {
	case resp.StatusCode == http.StatusNotImplemented:
		code = codes.Unimplemented
	case resp.StatusCode >= 500:
		code = codes.Unavailable
	case resp.StatusCode == http.StatusTooManyRequests:
		code = codes.ResourceExhausted
	case resp.StatusCode >= 400:
		code = codes.InvalidArgument
	default:
		// A 1xx/3xx here means a misbehaving engine (redirects are refused by
		// the client), not a bad request.
		code = codes.Unavailable
	}
	xlog.Warn("radiance: engine error", "path", path, "status", resp.StatusCode)
	return status.Error(code, fmt.Sprintf("radiance: engine %s returned %d: %s", path, resp.StatusCode, msg))
}
