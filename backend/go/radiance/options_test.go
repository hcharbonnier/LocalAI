package main

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	pb "github.com/mudler/LocalAI/pkg/grpc/proto"
)

func modelDirWithConfigJSON() string {
	dir := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600)).To(Succeed())
	return dir
}

func fullModelOptions(dir string, extra ...string) *pb.ModelOptions {
	opts := []string{
		"engine_path:/opt/radiance/bin/radiance-engine",
		"host:0.0.0.0",
		"port:8123",
		"api_key:sk-radiance",
		"cards:0,1",
		"tp:2",
		"max_model_len:8192",
		"max_num_seqs:4",
		"max_num_batched_tokens:1024",
		"kv_cache_dtype:fp8",
		"placement:interleave",
		"served_model_name:alias",
		"override_chat_template:/models/template.jinja",
		"num_speculative_tokens:3",
		"gpu_headroom_mib:512",
		"ready_timeout:2m",
		"stop_timeout:5s",
		"config_out:/tmp/radiance-out.yaml",
	}
	opts = append(opts, extra...)
	return &pb.ModelOptions{Model: "mymodel", ModelFile: dir, Options: opts}
}

var _ = Describe("options", func() {
	Describe("parseOptions", func() {
		It("parses every documented option", func() {
			c, err := parseOptions(fullModelOptions(modelDirWithConfigJSON()))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.enginePath).To(Equal("/opt/radiance/bin/radiance-engine"))
			Expect(c.host).To(Equal("0.0.0.0"))
			Expect(c.port).To(Equal(8123))
			Expect(c.apiKey).To(Equal("sk-radiance"))
			Expect(c.cards).To(Equal([]int{0, 1}))
			Expect(c.tp).To(Equal(2))
			Expect(c.maxModelLen).To(Equal(8192))
			Expect(c.maxNumSeqs).To(Equal(4))
			Expect(c.maxNumBatchedTokens).To(Equal(1024))
			Expect(c.kvCacheDtype).To(Equal("fp8"))
			Expect(c.placement).To(Equal("interleave"))
			Expect(c.servedModelName).To(Equal("alias"))
			Expect(c.overrideChatTemplate).To(Equal("/models/template.jinja"))
			Expect(c.numSpeculativeTokens).To(Equal(3))
			Expect(c.gpuHeadroomMiB).To(Equal(512))
			Expect(c.readyTimeout).To(Equal(2 * time.Minute))
			Expect(c.stopTimeout).To(Equal(5 * time.Second))
			Expect(c.configOut).To(Equal("/tmp/radiance-out.yaml"))
			Expect(c.modelName).To(Equal("mymodel"))
		})

		It("applies the documented defaults", func() {
			c, err := parseOptions(&pb.ModelOptions{Model: "owner/repo"})
			Expect(err).NotTo(HaveOccurred())
			Expect(c.host).To(Equal("127.0.0.1"))
			Expect(c.port).To(Equal(0))
			Expect(c.cards).To(Equal("all"))
			Expect(c.readyTimeout).To(Equal(900 * time.Second))
			Expect(c.stopTimeout).To(Equal(15 * time.Second))
			Expect(filepath.IsAbs(c.enginePath)).To(BeTrue())
			Expect(filepath.Base(c.enginePath)).To(Equal("radiance-engine"))
		})

		It("falls back to the environment for the engine path and API key", func() {
			GinkgoT().Setenv(envEnginePath, "/env/radiance-engine")
			GinkgoT().Setenv(envAPIKey, "sk-env")
			c, err := parseOptions(&pb.ModelOptions{Model: "owner/repo"})
			Expect(err).NotTo(HaveOccurred())
			Expect(c.enginePath).To(Equal("/env/radiance-engine"))
			Expect(c.apiKey).To(Equal("sk-env"))
		})

		It("prefers options over the environment", func() {
			GinkgoT().Setenv(envEnginePath, "/env/radiance-engine")
			GinkgoT().Setenv(envAPIKey, "sk-env")
			c, err := parseOptions(fullModelOptions(modelDirWithConfigJSON()))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.enginePath).To(Equal("/opt/radiance/bin/radiance-engine"))
			Expect(c.apiKey).To(Equal("sk-radiance"))
		})

		DescribeTable("rejects malformed values",
			func(opt string) {
				_, err := parseOptions(fullModelOptions(modelDirWithConfigJSON(), opt))
				Expect(err).To(HaveOccurred())
			},
			Entry("port", "port:http"),
			Entry("cards", "cards:all,1"),
			Entry("tp", "tp:-1"),
			Entry("ready_timeout", "ready_timeout:soon"),
			Entry("stop_timeout", "stop_timeout:0s"),
		)
	})

	Describe("model gate", func() {
		It("accepts a checkpoint directory", func() {
			_, _, err := locateModel(&pb.ModelOptions{Model: "m", ModelFile: modelDirWithConfigJSON()})
			Expect(err).NotTo(HaveOccurred())
		})

		It("accepts model files", func() {
			dir := GinkgoT().TempDir()
			for _, name := range []string{"m.rad", "m.gguf", "m.safetensors"} {
				p := filepath.Join(dir, name)
				Expect(os.WriteFile(p, []byte("x"), 0o600)).To(Succeed())
				_, _, err := locateModel(&pb.ModelOptions{Model: "m", ModelFile: p})
				Expect(err).NotTo(HaveOccurred())
			}
		})

		It("accepts an HF repo id", func() {
			artifact, _, err := locateModel(&pb.ModelOptions{Model: "owner/repo"})
			Expect(err).NotTo(HaveOccurred())
			Expect(artifact).To(Equal("owner/repo"))
		})

		It("resolves a relative path under the models directory", func() {
			dir := modelDirWithConfigJSON()
			opts := &pb.ModelOptions{Model: "m", ModelFile: "m", ModelPath: dir}
			// The directory itself is named "m" under ModelPath.
			Expect(os.MkdirAll(filepath.Join(dir, "m"), 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "m", "config.json"), []byte("{}"), 0o600)).To(Succeed())
			artifact, _, err := locateModel(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(artifact).To(Equal(filepath.Join(dir, "m")))
		})

		It("rejects a bare name and an unsupported directory", func() {
			_, _, err := locateModel(&pb.ModelOptions{Model: "some-model"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("HF repo id"))

			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)).To(Succeed())
			_, _, err = locateModel(&pb.ModelOptions{Model: "m", ModelFile: dir})
			Expect(err).To(HaveOccurred())
		})

		It("rejects an existing but unsupported path under the models directory", func() {
			dir := GinkgoT().TempDir()
			Expect(os.MkdirAll(filepath.Join(dir, "m"), 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "m", "notes.txt"), []byte("x"), 0o600)).To(Succeed())
			_, _, err := locateModel(&pb.ModelOptions{Model: "m", ModelFile: "m", ModelPath: dir})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not a"))
		})

		It("rejects an empty model", func() {
			_, _, err := locateModel(&pb.ModelOptions{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("render", func() {
		It("writes every option into the YAML", func() {
			c, err := parseOptions(fullModelOptions(modelDirWithConfigJSON()))
			Expect(err).NotTo(HaveOccurred())
			raw, err := c.render()
			Expect(err).NotTo(HaveOccurred())

			var out map[string]any
			Expect(yaml.Unmarshal(raw, &out)).To(Succeed())
			server := out["server"].(map[string]any)
			Expect(server["host"]).To(Equal("0.0.0.0"))
			Expect(server["port"]).To(Equal(8123))
			Expect(server["api-key"]).To(Equal("sk-radiance"))
			Expect(out["cards"]).To(Equal([]any{0, 1}))
			Expect(out["memory"]).To(Equal(map[string]any{"gpu-headroom-mib": 512}))

			models := out["models"].(map[string]any)
			m := models["mymodel"].(map[string]any)
			Expect(m["model"]).To(Equal(c.model))
			Expect(m["tp"]).To(Equal(2))
			Expect(m["max-model-len"]).To(Equal(8192))
			Expect(m["max-num-seqs"]).To(Equal(4))
			Expect(m["max-num-batched-tokens"]).To(Equal(1024))
			Expect(m["kv-cache-dtype"]).To(Equal("fp8"))
			Expect(m["placement"]).To(Equal("interleave"))
			Expect(m["served-model-name"]).To(Equal("alias"))
			Expect(m["override-chat-template"]).To(Equal("/models/template.jinja"))
			Expect(m["num-speculative-tokens"]).To(Equal(3))
		})

		It("defaults cards to all and omits unset settings", func() {
			c, err := parseOptions(&pb.ModelOptions{Model: "owner/repo"})
			Expect(err).NotTo(HaveOccurred())
			raw, err := c.render()
			Expect(err).NotTo(HaveOccurred())

			var out map[string]any
			Expect(yaml.Unmarshal(raw, &out)).To(Succeed())
			Expect(out["cards"]).To(Equal("all"))
			Expect(out).NotTo(HaveKey("memory"))
			m := out["models"].(map[string]any)["owner/repo"].(map[string]any)
			Expect(m).To(HaveLen(1))
			Expect(m["model"]).To(Equal("owner/repo"))
		})

		It("uses config_template instead of the generated YAML", func() {
			dir := GinkgoT().TempDir()
			tmpl := filepath.Join(dir, "radiance.tmpl")
			body := "server:\n  host: {{.Host}}\n  port: {{.Port}}\n  api-key: {{.APIKey}}\nmodels:\n  {{.ModelName}}:\n    model: {{.Model}}\n"
			Expect(os.WriteFile(tmpl, []byte(body), 0o600)).To(Succeed())

			c, err := parseOptions(fullModelOptions(modelDirWithConfigJSON(), "config_template:"+tmpl))
			Expect(err).NotTo(HaveOccurred())
			raw, err := c.render()
			Expect(err).NotTo(HaveOccurred())
			out := string(raw)
			Expect(out).To(ContainSubstring("host: 0.0.0.0"))
			Expect(out).To(ContainSubstring("port: 8123"))
			Expect(out).To(ContainSubstring("api-key: sk-radiance"))
			Expect(out).To(ContainSubstring("mymodel:"))
			Expect(out).To(ContainSubstring("model: " + c.model))
			Expect(out).NotTo(ContainSubstring("cards"))
		})

		It("reports a broken or missing config_template", func() {
			c := &options{configTemplate: filepath.Join(GinkgoT().TempDir(), "missing.tmpl")}
			_, err := c.render()
			Expect(err).To(HaveOccurred())

			dir := GinkgoT().TempDir()
			bad := filepath.Join(dir, "bad.tmpl")
			Expect(os.WriteFile(bad, []byte("{{.Nope}"), 0o600)).To(Succeed())
			c = &options{configTemplate: bad}
			_, err = c.render()
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("choosePort", func() {
		It("keeps a configured port and picks a free one otherwise", func() {
			c := &options{host: "127.0.0.1", port: 8123}
			p, err := c.choosePort()
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(8123))

			c = &options{host: "0.0.0.0"}
			p, err = c.choosePort()
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(BeNumerically(">", 0))
		})
	})

	It("normalizes wildcard probe hosts", func() {
		Expect(probeHost("")).To(Equal("127.0.0.1"))
		Expect(probeHost("0.0.0.0")).To(Equal("127.0.0.1"))
		Expect(probeHost("::")).To(Equal("::1"))
		Expect(probeHost("10.0.0.5")).To(Equal("10.0.0.5"))
	})
})

var _ = Describe("parseCards", func() {
	It("maps all and index lists", func() {
		v, err := parseCards("")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("all"))
		v, err = parseCards("all")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("all"))
		v, err = parseCards(" 2, 0 ")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal([]int{2, 0}))
	})
})

var _ = Describe("isRepoID", func() {
	It("accepts only owner/name", func() {
		Expect(isRepoID("owner/name")).To(BeTrue())
		Expect(isRepoID("some-model")).To(BeFalse())
		Expect(isRepoID("/abs/path")).To(BeFalse())
		Expect(isRepoID("a/b/c")).To(BeFalse())
		Expect(isRepoID("owner/")).To(BeFalse())
		Expect(isRepoID("a b/c")).To(BeFalse())
	})
})
