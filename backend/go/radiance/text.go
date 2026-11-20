package main

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/mudler/xlog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
)

// textRequest is the body shared by /v1/chat/completions and
// /v1/completions. Zero sampling values are omitted so radiance's own defaults
// apply; temperature is always sent because 0 is a real choice (greedy) and
// core always fills it.
type textRequest struct {
	Model            string          `json:"model"`
	Messages         []chatMessage   `json:"messages,omitempty"`
	Prompt           string          `json:"prompt,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	MaxTokens        int32           `json:"max_tokens,omitempty"`
	Temperature      float32         `json:"temperature"`
	TopP             float32         `json:"top_p,omitempty"`
	TopK             int32           `json:"top_k,omitempty"`
	MinP             float32         `json:"min_p,omitempty"`
	Seed             int32           `json:"seed,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	FrequencyPenalty float32         `json:"frequency_penalty,omitempty"`
	PresencePenalty  float32         `json:"presence_penalty,omitempty"`
	Tools            json.RawMessage `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat   json.RawMessage `json:"response_format,omitempty"`
	StreamOptions    *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// replyChoice covers both endpoints: chat fills Message (Delta when
// streaming), completions fills Text.
type replyChoice struct {
	Text    string      `json:"text"`
	Message choiceDelta `json:"message"`
	Delta   choiceDelta `json:"delta"`
}

type choiceDelta struct {
	Content          string          `json:"content"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []replyToolCall `json:"tool_calls"`
}

type replyToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type usage struct {
	PromptTokens     int32 `json:"prompt_tokens"`
	CompletionTokens int32 `json:"completion_tokens"`
}

type textResponse struct {
	// Error is set on a frame the engine sends when generation fails after the
	// stream has started.
	Error   *struct{ Message string } `json:"error"`
	Choices []replyChoice             `json:"choices"`
	Usage   *usage                    `json:"usage"`
}

// textRequest picks the chat endpoint when core sent structured messages;
// otherwise the prompt is already rendered and completions takes it verbatim.
func (ep *endpoint) textRequest(opts *pb.PredictOptions, stream bool) (string, *textRequest) {
	if dropped := unforwardedFields(opts); len(dropped) > 0 {
		xlog.Warn("radiance: request fields are not forwarded", "fields", dropped)
	}
	req := &textRequest{
		Model:            ep.model,
		Stream:           stream,
		MaxTokens:        opts.GetTokens(),
		Temperature:      opts.GetTemperature(),
		TopP:             opts.GetTopP(),
		TopK:             opts.GetTopK(),
		MinP:             opts.GetMinP(),
		Seed:             opts.GetSeed(),
		Stop:             opts.GetStopPrompts(),
		FrequencyPenalty: opts.GetFrequencyPenalty(),
		PresencePenalty:  opts.GetPresencePenalty(),
		Tools:            rawJSON(opts.GetTools()),
		ToolChoice:       rawJSON(opts.GetToolChoice()),
		ResponseFormat:   responseFormat(opts),
	}
	if stream {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if len(opts.GetMessages()) == 0 {
		req.Prompt = opts.GetPrompt()
		return "/v1/completions", req
	}
	for _, m := range opts.GetMessages() {
		msg := chatMessage{
			Role:       m.GetRole(),
			Content:    m.GetContent(),
			Name:       m.GetName(),
			ToolCallID: m.GetToolCallId(),
		}
		// A previous assistant turn carries its tool calls as a JSON string.
		if tc := m.GetToolCalls(); tc != "" {
			if err := json.Unmarshal([]byte(tc), &msg.ToolCalls); err != nil {
				xlog.Debug("radiance: drop malformed tool_calls on message", "error", err)
			}
		}
		req.Messages = append(req.Messages, msg)
	}
	return "/v1/chat/completions", req
}

// responseFormat forwards a caller's response_format when core passed one in
// the request metadata. Core otherwise compiles json_object/json_schema into a
// grammar, which radiance takes as a grammar response format.
func responseFormat(opts *pb.PredictOptions) json.RawMessage {
	if v := opts.GetMetadata()["response_format"]; v != "" {
		if json.Valid([]byte(v)) {
			return json.RawMessage(v)
		}
		xlog.Warn("radiance: ignoring malformed response_format metadata")
	}
	if g := opts.GetGrammar(); g != "" {
		raw, err := json.Marshal(map[string]string{"type": "grammar", "grammar": g})
		if err == nil {
			return raw
		}
	}
	return nil
}

func rawJSON(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

// unforwardedFields names request inputs this backend cannot send. Dropping
// them silently would answer a text-only question for a multimodal request.
func unforwardedFields(opts *pb.PredictOptions) []string {
	var out []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"images", len(opts.GetImages()) > 0},
		{"audios", len(opts.GetAudios()) > 0},
		{"videos", len(opts.GetVideos()) > 0},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// replyFromChoice builds the Reply for one choice. Reasoning and tool calls
// travel as ChatDeltas, the shape core already understands from other
// backends.
func replyFromChoice(c replyChoice, streaming bool) *pb.Reply {
	d := c.Message
	if streaming {
		d = c.Delta
	}
	content := d.Content
	if content == "" {
		content = c.Text
	}
	reasoning := d.Reasoning
	if reasoning == "" {
		reasoning = d.ReasoningContent
	}

	reply := &pb.Reply{Message: []byte(content)}
	if reasoning == "" && len(d.ToolCalls) == 0 && (!streaming || content == "") {
		return reply
	}
	delta := &pb.ChatDelta{Content: content, ReasoningContent: reasoning}
	for _, tc := range d.ToolCalls {
		delta.ToolCalls = append(delta.ToolCalls, &pb.ToolCallDelta{
			Index:     clampInt32(tc.Index),
			Id:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	reply.ChatDeltas = []*pb.ChatDelta{delta}
	return reply
}

func applyUsage(reply *pb.Reply, u *usage) {
	if u == nil {
		return
	}
	reply.PromptTokens = u.PromptTokens
	reply.Tokens = u.CompletionTokens
}

func (r *Radiance) PredictRich(opts *pb.PredictOptions) (*pb.Reply, error) {
	return r.PredictRichContext(context.Background(), opts)
}

func (r *Radiance) PredictRichContext(ctx context.Context, opts *pb.PredictOptions) (*pb.Reply, error) {
	ep, err := r.endpoint()
	if err != nil {
		return nil, err
	}
	path, body := ep.textRequest(opts, false)
	var resp textResponse
	if err := r.postJSON(ctx, ep, path, body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, status.Errorf(codes.Unavailable, "radiance: engine %s failed: %s", path, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return nil, status.Errorf(codes.Internal, "radiance: engine %s returned no choices", path)
	}
	reply := replyFromChoice(resp.Choices[0], false)
	applyUsage(reply, resp.Usage)
	return reply, nil
}

func (r *Radiance) PredictStreamRich(opts *pb.PredictOptions, results chan<- *pb.Reply) error {
	return r.PredictStreamRichContext(context.Background(), opts, results)
}

// PredictStreamRichContext sends one Reply per engine SSE delta and does not
// close results: the gRPC server does, after this returns.
func (r *Radiance) PredictStreamRichContext(ctx context.Context, opts *pb.PredictOptions, results chan<- *pb.Reply) error {
	ep, err := r.endpoint()
	if err != nil {
		return err
	}
	path, body := ep.textRequest(opts, true)
	resp, err := r.postStream(ctx, ep, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	put := func(reply *pb.Reply) error {
		select {
		case results <- reply:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// An engine that ignores stream=true answers with one JSON body. Emitting
	// it keeps the caller from receiving an empty stream.
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		var whole textResponse
		if err := json.NewDecoder(resp.Body).Decode(&whole); err != nil {
			return status.Errorf(codes.Internal, "radiance: engine %s returned neither an SSE stream nor JSON: %v", path, err)
		}
		if whole.Error != nil {
			return status.Errorf(codes.Unavailable, "radiance: engine %s failed: %s", path, whole.Error.Message)
		}
		if len(whole.Choices) == 0 && whole.Usage == nil {
			return status.Errorf(codes.Internal, "radiance: engine %s returned no choices", path)
		}
		for _, c := range whole.Choices {
			reply := replyFromChoice(c, false)
			if len(reply.GetMessage()) == 0 && len(reply.GetChatDeltas()) == 0 {
				continue
			}
			if err := put(reply); err != nil {
				return err
			}
		}
		if whole.Usage != nil {
			return put(usageReply(whole.Usage))
		}
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	// A single frame can carry a long tool-call argument chunk.
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		payload, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			return nil
		}
		if payload == "" {
			continue
		}
		var chunk textResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			xlog.Debug("radiance: skip malformed SSE frame", "path", path, "error", err)
			continue
		}
		if chunk.Error != nil {
			xlog.Warn("radiance: engine stream error", "path", path, "error", chunk.Error.Message)
			return status.Errorf(codes.Unavailable, "radiance: engine %s stream failed: %s", path, chunk.Error.Message)
		}
		for _, c := range chunk.Choices {
			reply := replyFromChoice(c, true)
			if len(reply.GetMessage()) == 0 && len(reply.GetChatDeltas()) == 0 {
				continue
			}
			if err := put(reply); err != nil {
				return err
			}
		}
		// include_usage normally arrives as its own frame with no choices, but
		// some servers attach it to the last content frame.
		if chunk.Usage != nil {
			if err := put(usageReply(chunk.Usage)); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return transportError(path, err)
	}
	return nil
}

func usageReply(u *usage) *pb.Reply {
	return &pb.Reply{PromptTokens: u.PromptTokens, Tokens: u.CompletionTokens}
}

// Predict is the legacy string path; the gRPC server prefers PredictRich.
func (r *Radiance) Predict(opts *pb.PredictOptions) (string, error) {
	reply, err := r.PredictRich(opts)
	if err != nil {
		return "", err
	}
	return string(reply.GetMessage()), nil
}

// PredictStream owns and closes results, per the legacy AIModel contract.
func (r *Radiance) PredictStream(opts *pb.PredictOptions, results chan string) error {
	defer close(results)
	rich := make(chan *pb.Reply)
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.PredictStreamRich(opts, rich)
		close(rich)
	}()
	for reply := range rich {
		if msg := reply.GetMessage(); len(msg) > 0 {
			results <- string(msg)
		}
	}
	return <-errCh
}

// clampInt32 narrows an engine-supplied int (token counts, tool-call indexes)
// to the int32 the protocol carries. The engine is another process, so an
// absurd value must saturate rather than wrap.
func clampInt32(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	}
	return int32(n)
}
