package main

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc/codes"

	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
)

func stubLoadOptions(mode, configOut string) (*pb.ModelOptions, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	opts := []string{
		"engine_path:" + exe,
		"ready_timeout:5s",
		"stop_timeout:2s",
	}
	if configOut != "" {
		opts = append(opts, "config_out:"+configOut)
	}
	return &pb.ModelOptions{
		Model:     "stub-model",
		ModelFile: modelDirWithConfigJSON(),
		Options:   opts,
		EnvVars:   map[string]string{"RADIANCE_STUB_MODE": mode},
	}, nil
}

var _ = Describe("Radiance", func() {
	It("loads the engine, predicts, reports status and frees it", func() {
		out := filepath.Join(GinkgoT().TempDir(), "engine.yaml")
		opts, err := stubLoadOptions("ok", out)
		Expect(err).NotTo(HaveOccurred())

		r := NewRadiance()
		Expect(r.Load(opts)).To(Succeed())
		DeferCleanup(func() { _ = r.Free() })

		st, err := r.Status()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.GetState()).To(Equal(pb.StatusResponse_READY))

		reply, err := r.PredictRich(&pb.PredictOptions{
			Messages: []*pb.Message{{Role: "user", Content: "hi"}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(reply.GetMessage())).To(Equal("stub answer"))
		Expect(reply.GetPromptTokens()).To(Equal(int32(2)))

		raw, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring("stub-model"))
		Expect(string(raw)).To(ContainSubstring("server:"))

		Expect(r.Free()).To(Succeed())
		st, err = r.Status()
		Expect(err).NotTo(HaveOccurred())
		Expect(st.GetState()).To(Equal(pb.StatusResponse_UNINITIALIZED))
		_, err = r.PredictRich(&pb.PredictOptions{Prompt: "x"})
		Expect(codeOf(err)).To(Equal(codes.FailedPrecondition))
	})

	It("streams through the stub engine", func() {
		opts, err := stubLoadOptions("ok", "")
		Expect(err).NotTo(HaveOccurred())
		r := NewRadiance()
		Expect(r.Load(opts)).To(Succeed())
		DeferCleanup(func() { _ = r.Free() })

		results := make(chan *pb.Reply, 10)
		Expect(r.PredictStreamRich(&pb.PredictOptions{
			Messages: []*pb.Message{{Role: "user", Content: "hi"}},
		}, results)).To(Succeed())
		var text strings.Builder
		var prompt, completion int32
		for len(results) > 0 {
			reply := <-results
			text.Write(reply.GetMessage())
			if reply.GetPromptTokens() > 0 {
				prompt, completion = reply.GetPromptTokens(), reply.GetTokens()
			}
		}
		Expect(text.String()).To(Equal("stub answer"))
		Expect(prompt).To(Equal(int32(2)))
		Expect(completion).To(Equal(int32(2)))
	})

	It("fails the load and leaves no config when the engine is missing", func() {
		opts := &pb.ModelOptions{
			Model:     "stub-model",
			ModelFile: modelDirWithConfigJSON(),
			Options: []string{
				"engine_path:" + filepath.Join(GinkgoT().TempDir(), "missing-engine"),
			},
		}
		err := NewRadiance().Load(opts)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not available"))
	})
})
