package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRadiance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "radiance backend test suite")
}
