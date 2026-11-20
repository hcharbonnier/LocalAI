package main

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
)

func codeOf(err error) codes.Code {
	st, ok := status.FromError(err)
	if !ok {
		return codes.Unknown
	}
	return st.Code()
}

var _ = Describe("text", func() {
	var up *fakeUpstream

	BeforeEach(func() {
		up = newFakeUpstream()
		DeferCleanup(up.Close)
	})

	Describe("endpoint selection", func() {
		It("uses chat when messages are present and completions for a bare prompt", func() {
			ep := &endpoint{baseURL: "http://engine", model: "m"}
			path, body := ep.textRequest(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "hi"}},
			}, false)
			Expect(path).To(Equal("/v1/chat/completions"))
			Expect(body.Messages).To(HaveLen(1))

			path, body = ep.textRequest(&pb.PredictOptions{Prompt: "once upon"}, false)
			Expect(path).To(Equal("/v1/completions"))
			Expect(body.Prompt).To(Equal("once upon"))
		})
	})

	Describe("PredictRich", func() {
		It("maps the request and returns content, reasoning and usage", func() {
			up.replyJSON("/v1/chat/completions", map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": "hello back", "reasoning_content": "thinking",
				}}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
			})
			r := testBackend(up.URL)

			reply, err := r.PredictRich(&pb.PredictOptions{
				Messages: []*pb.Message{
					{Role: "system", Content: "be nice"},
					{Role: "user", Content: "hello"},
				},
				Tokens:           32,
				Temperature:      0.5,
				TopP:             0.9,
				TopK:             40,
				MinP:             0.05,
				Seed:             9,
				StopPrompts:      []string{"</s>"},
				Tools:            `[{"type":"function","function":{"name":"get_weather"}}]`,
				ToolChoice:       `"auto"`,
				FrequencyPenalty: 0.2,
				PresencePenalty:  0.1,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(reply.GetMessage())).To(Equal("hello back"))
			Expect(reply.GetChatDeltas()).To(HaveLen(1))
			Expect(reply.GetChatDeltas()[0].GetReasoningContent()).To(Equal("thinking"))
			Expect(reply.GetPromptTokens()).To(Equal(int32(3)))
			Expect(reply.GetTokens()).To(Equal(int32(2)))

			req := up.last()
			Expect(req.Method).To(Equal(http.MethodPost))
			Expect(req.Path).To(Equal("/v1/chat/completions"))
			Expect(req.JSON).To(HaveKeyWithValue("model", "stub-model"))
			Expect(req.JSON).To(HaveKeyWithValue("max_tokens", BeNumerically("==", 32)))
			Expect(req.JSON).To(HaveKeyWithValue("temperature", BeNumerically("==", 0.5)))
			Expect(req.JSON).To(HaveKeyWithValue("top_p", BeNumerically("==", 0.9)))
			Expect(req.JSON).To(HaveKeyWithValue("top_k", BeNumerically("==", 40)))
			Expect(req.JSON).To(HaveKeyWithValue("min_p", BeNumerically("==", 0.05)))
			Expect(req.JSON).To(HaveKeyWithValue("seed", BeNumerically("==", 9)))
			Expect(req.JSON).To(HaveKeyWithValue("stop", ConsistOf("</s>")))
			Expect(req.JSON).To(HaveKeyWithValue("frequency_penalty", BeNumerically("==", 0.2)))
			Expect(req.JSON).To(HaveKeyWithValue("presence_penalty", BeNumerically("==", 0.1)))
			Expect(req.JSON).To(HaveKey("tools"))
			Expect(req.JSON).To(HaveKeyWithValue("tool_choice", "auto"))
			Expect(req.JSON).NotTo(HaveKey("stream"))
			Expect(req.JSON["messages"]).To(ConsistOf(
				HaveKeyWithValue("role", "system"),
				HaveKeyWithValue("role", "user"),
			))
		})

		It("always sends temperature, even 0", func() {
			up.replyJSON("/v1/completions", map[string]any{"choices": []any{map[string]any{"text": "t"}}})
			r := testBackend(up.URL)
			_, err := r.PredictRich(&pb.PredictOptions{Prompt: "x"})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.last().JSON).To(HaveKeyWithValue("temperature", BeNumerically("==", 0)))
		})

		It("returns tool calls as chat deltas", func() {
			up.replyJSON("/v1/chat/completions", map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "call_1", "type": "function",
						"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Rome"}`},
					}},
				}}},
			})
			r := testBackend(up.URL)
			reply, err := r.PredictRich(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "weather?"}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(reply.GetChatDeltas()).To(HaveLen(1))
			tc := reply.GetChatDeltas()[0].GetToolCalls()
			Expect(tc).To(HaveLen(1))
			Expect(tc[0].GetId()).To(Equal("call_1"))
			Expect(tc[0].GetName()).To(Equal("get_weather"))
			Expect(tc[0].GetArguments()).To(Equal(`{"city":"Rome"}`))
		})

		It("parses assistant tool calls in the request history", func() {
			up.replyJSON("/v1/chat/completions", map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}},
			})
			r := testBackend(up.URL)
			_, err := r.PredictRich(&pb.PredictOptions{Messages: []*pb.Message{
				{Role: "assistant", ToolCalls: `[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]`},
				{Role: "tool", ToolCallId: "call_1", Content: "42"},
			}})
			Expect(err).NotTo(HaveOccurred())
			msgs := up.last().JSON["messages"].([]any)
			first := msgs[0].(map[string]any)
			Expect(first).To(HaveKey("tool_calls"))
			Expect(msgs[1]).To(HaveKeyWithValue("tool_call_id", "call_1"))
		})

		It("sends a bare prompt to /v1/completions", func() {
			up.replyJSON("/v1/completions", map[string]any{
				"choices": []any{map[string]any{"text": "completed"}},
			})
			r := testBackend(up.URL)
			reply, err := r.PredictRich(&pb.PredictOptions{Prompt: "once upon"})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(reply.GetMessage())).To(Equal("completed"))
			req := up.last()
			Expect(req.Path).To(Equal("/v1/completions"))
			Expect(req.JSON).To(HaveKeyWithValue("prompt", "once upon"))
		})

		It("forwards response_format from metadata and otherwise from the grammar", func() {
			up.replyJSON("/v1/chat/completions", map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "{}"}}},
			})
			r := testBackend(up.URL)

			_, err := r.PredictRich(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "json"}},
				Metadata: map[string]string{"response_format": `{"type":"json_object"}`},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.last().JSON).To(HaveKeyWithValue("response_format", HaveKeyWithValue("type", "json_object")))

			_, err = r.PredictRich(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "json"}},
				Grammar:  "root ::= \"x\"",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(up.last().JSON).To(HaveKeyWithValue("response_format", HaveKeyWithValue("type", "grammar")))
		})

		It("fails when the engine returns no choices", func() {
			up.replyJSON("/v1/completions", map[string]any{"choices": []any{}})
			r := testBackend(up.URL)
			_, err := r.PredictRich(&pb.PredictOptions{Prompt: "x"})
			Expect(codeOf(err)).To(Equal(codes.Internal))
		})

		It("reports an unloaded backend as FailedPrecondition", func() {
			_, err := NewRadiance().PredictRich(&pb.PredictOptions{Prompt: "x"})
			Expect(codeOf(err)).To(Equal(codes.FailedPrecondition))
		})

		DescribeTable("maps engine errors to gRPC codes and keeps the body",
			func(status int, body string, want codes.Code) {
				up.script("/v1/completions", scriptedResponse{Status: status, Body: body})
				r := testBackend(up.URL)
				_, err := r.PredictRich(&pb.PredictOptions{Prompt: "x"})
				Expect(codeOf(err)).To(Equal(want))
				Expect(err.Error()).To(ContainSubstring(body))
			},
			Entry("500", http.StatusInternalServerError, "engine exploded", codes.Unavailable),
			Entry("503", http.StatusServiceUnavailable, "not ready", codes.Unavailable),
			Entry("429", http.StatusTooManyRequests, "slow down", codes.ResourceExhausted),
			Entry("400", http.StatusBadRequest, "bad request", codes.InvalidArgument),
			Entry("501", http.StatusNotImplemented, "no embedding head", codes.Unimplemented),
		)

		It("truncates a long error body", func() {
			long := make([]byte, 4000)
			for i := range long {
				long[i] = 'a'
			}
			up.script("/v1/completions", scriptedResponse{Status: http.StatusInternalServerError, Body: string(long)})
			r := testBackend(up.URL)
			_, err := r.PredictRich(&pb.PredictOptions{Prompt: "x"})
			Expect(codeOf(err)).To(Equal(codes.Unavailable))
			Expect(len(status.Convert(err).Message())).To(BeNumerically("<", 700))
		})
	})

	Describe("PredictStreamRich", func() {
		It("streams content, reasoning and tool-call deltas and leaves the channel open", func() {
			up.script("/v1/chat/completions", scriptedResponse{SSE: []string{
				sseJSON(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}}}}),
				sseJSON(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "think"}}}}),
				sseJSON(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hel"}}}}),
				sseJSON(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "call_1", "type": "function",
						"function": map[string]any{"name": "get_weather", "arguments": `{"city"`},
					}},
				}}}}),
				sseJSON(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3}}),
				"[DONE]",
			}})
			r := testBackend(up.URL)

			results := make(chan *pb.Reply, 10)
			err := r.PredictStreamRich(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "hi"}},
			}, results)
			Expect(err).NotTo(HaveOccurred())

			var got []*pb.Reply
			for len(results) > 0 {
				got = append(got, <-results)
			}
			Expect(got).To(HaveLen(4))
			Expect(got[0].GetChatDeltas()[0].GetReasoningContent()).To(Equal("think"))
			Expect(string(got[1].GetMessage())).To(Equal("Hel"))
			tc := got[2].GetChatDeltas()[0].GetToolCalls()
			Expect(tc).To(HaveLen(1))
			Expect(tc[0].GetName()).To(Equal("get_weather"))
			Expect(tc[0].GetArguments()).To(Equal(`{"city"`))
			Expect(got[3].GetPromptTokens()).To(Equal(int32(7)))
			Expect(got[3].GetTokens()).To(Equal(int32(3)))

			req := up.last()
			Expect(req.JSON).To(HaveKeyWithValue("stream", true))
			Expect(req.JSON).To(HaveKeyWithValue("stream_options", HaveKeyWithValue("include_usage", true)))
			// The gRPC server closes the channel; closing it here must not panic.
			close(results)
		})

		It("streams completions text for a bare prompt", func() {
			up.script("/v1/completions", scriptedResponse{SSE: []string{
				sseJSON(map[string]any{"choices": []any{map[string]any{"text": "a"}}}),
				sseJSON(map[string]any{"choices": []any{map[string]any{"text": "b"}}}),
				"[DONE]",
			}})
			r := testBackend(up.URL)
			results := make(chan *pb.Reply, 10)
			Expect(r.PredictStreamRich(&pb.PredictOptions{Prompt: "go"}, results)).To(Succeed())
			Expect(results).To(HaveLen(2))
			Expect(string((<-results).GetMessage())).To(Equal("a"))
			Expect(string((<-results).GetMessage())).To(Equal("b"))
		})

		It("returns a mid-stream error frame as Unavailable", func() {
			up.script("/v1/chat/completions", scriptedResponse{SSE: []string{
				sseJSON(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "partial"}}}}),
				sseJSON(map[string]any{"error": map[string]any{"message": "engine crashed"}}),
				"[DONE]",
			}})
			r := testBackend(up.URL)
			results := make(chan *pb.Reply, 10)
			err := r.PredictStreamRich(&pb.PredictOptions{
				Messages: []*pb.Message{{Role: "user", Content: "hi"}},
			}, results)
			Expect(codeOf(err)).To(Equal(codes.Unavailable))
			Expect(err.Error()).To(ContainSubstring("engine crashed"))
			Expect(results).To(HaveLen(1))
		})

		It("maps a failing engine to a gRPC code", func() {
			up.script("/v1/completions", scriptedResponse{Status: http.StatusBadGateway, Body: "gateway"})
			r := testBackend(up.URL)
			err := r.PredictStreamRich(&pb.PredictOptions{Prompt: "go"}, make(chan *pb.Reply, 1))
			Expect(codeOf(err)).To(Equal(codes.Unavailable))
		})

		It("stops when the caller cancels", func() {
			up.script("/v1/completions", scriptedResponse{SSE: []string{
				sseJSON(map[string]any{"choices": []any{map[string]any{"text": "a"}}}),
			}})
			r := testBackend(up.URL)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			results := make(chan *pb.Reply, 1)
			err := r.PredictStreamRichContext(ctx, &pb.PredictOptions{Prompt: "go"}, results)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("legacy Predict and PredictStream", func() {
		It("wrap the rich variants", func() {
			up.replyJSON("/v1/completions", map[string]any{"choices": []any{map[string]any{"text": "plain"}}})
			r := testBackend(up.URL)
			out, err := r.Predict(&pb.PredictOptions{Prompt: "x"})
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal("plain"))

			up.script("/v1/completions", scriptedResponse{SSE: []string{
				sseJSON(map[string]any{"choices": []any{map[string]any{"text": "s1"}}}),
				"[DONE]",
			}})
			results := make(chan string, 10)
			Expect(r.PredictStream(&pb.PredictOptions{Prompt: "x"}, results)).To(Succeed())
			var got []string
			for s := range results { // PredictStream closes the channel
				got = append(got, s)
			}
			Expect(got).To(Equal([]string{"s1"}))
		})
	})
})

var _ = Describe("clampInt32", func() {
	It("passes in-range values through and saturates the rest", func() {
		Expect(clampInt32(0)).To(Equal(int32(0)))
		Expect(clampInt32(42)).To(Equal(int32(42)))
		Expect(clampInt32(-7)).To(Equal(int32(-7)))
	})
})
