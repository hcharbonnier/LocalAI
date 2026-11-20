package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mudler/LocalAI/pkg/httpclient"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("engine", func() {
	It("becomes ready and stops", func() {
		cfg := stubEngineConfig(GinkgoT().TempDir(), "ok", 5*time.Second)
		cfg.Client = httpclient.New()
		e, err := startEngine(cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(e.alive()).To(BeTrue())
		Expect(e.pid()).To(BeNumerically(">", 0))
		e.close()
		Expect(e.alive()).To(BeFalse())
	})

	It("times out and kills an engine that never turns ready", func() {
		cfg := stubEngineConfig(GinkgoT().TempDir(), "never", 400*time.Millisecond)
		cfg.Client = httpclient.New()
		start := time.Now()
		_, err := startEngine(cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("did not become ready"))
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
	})

	It("carries the engine's stderr when it exits early", func() {
		cfg := stubEngineConfig(GinkgoT().TempDir(), "exit", 5*time.Second)
		cfg.Client = httpclient.New()
		_, err := startEngine(cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exited before it was ready"))
		Expect(err.Error()).To(ContainSubstring("cannot load model"))
	})

	It("lets the model environment override the inherited one", func() {
		GinkgoT().Setenv("RADIANCE_STUB_PROBE", "inherited")
		log := filepath.Join(GinkgoT().TempDir(), "probe")
		cfg := stubEngineConfig(GinkgoT().TempDir(), "ok", 5*time.Second)
		cfg.Env = append(cfg.Env, "RADIANCE_STUB_PROBE=override", "RADIANCE_STUB_ENV_LOG="+log)
		cfg.Client = httpclient.New()
		e, err := startEngine(cfg)
		Expect(err).NotTo(HaveOccurred())
		defer e.close()

		raw, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(Equal("override"))
	})

	It("refuses a missing executable", func() {
		cfg := stubEngineConfig(GinkgoT().TempDir(), "ok", time.Second)
		cfg.Path = filepath.Join(GinkgoT().TempDir(), "no-such-engine")
		_, err := startEngine(cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not available"))
	})

	It("refuses an empty base URL before starting the child", func() {
		cfg := stubEngineConfig(GinkgoT().TempDir(), "ok", time.Second)
		cfg.BaseURL = ""
		_, err := startEngine(cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("base URL is empty"))
	})

	It("keeps its log tail bounded", func() {
		t := &tailBuffer{maxLines: 3, maxBytes: 8}
		for _, l := range []string{"one", "two", "three", "four"} {
			t.add("stderr", l)
		}
		t.add("stdout", strings.Repeat("x", 20))
		got := t.String()
		Expect(strings.Split(got, "\n")).To(HaveLen(3))
		Expect(got).NotTo(ContainSubstring("one"))
		Expect(got).To(ContainSubstring("..."))
	})

	It("lets extra environment entries override inherited keys", func() {
		Expect(mergeEnv([]string{"A=1", "B=2"}, []string{"B=3", "C=4"})).
			To(ConsistOf("A=1", "B=3", "C=4"))
	})

	It("assembles lines from stream chunks and bounds them", func() {
		tail := &tailBuffer{maxLines: 10, maxBytes: 8}
		w := newLineWriter(tail, "stderr")
		_, _ = w.Write([]byte("par"))
		_, _ = w.Write([]byte("tial\nlong line that is cut\n"))
		w.flush()
		got := tail.String()
		Expect(strings.Split(got, "\n")).To(HaveLen(2))
		Expect(got).To(ContainSubstring("stderr: partial"))
		Expect(got).To(ContainSubstring("..."))
	})
})
