package main

// radiance is a LocalAI backend for the Radiance inference engine
// (https://codeberg.org/StillDeadcode/radiance, Apache-2.0). It runs the
// engine as a child process with a generated YAML config and proxies requests
// to its OpenAI-compatible HTTP API. See README.md.

import (
	"flag"
	"os"

	grpc "github.com/mudler/LocalAI/pkg/grpc"
	"github.com/mudler/xlog"
	"golang.org/x/term"
)

var addr = flag.String("addr", "localhost:50051", "the address to listen on")

func main() {
	// xlog's default handler emits ANSI colors, which are unreadable once
	// LocalAI captures the backend's stdout into a log file.
	format := os.Getenv("LOCALAI_LOG_FORMAT")
	if format == "" && !term.IsTerminal(int(os.Stdout.Fd())) {
		format = xlog.TextFormat
	}
	xlog.SetLogger(xlog.NewLogger(xlog.LogLevel(os.Getenv("LOCALAI_LOG_LEVEL")), format))
	flag.Parse()
	if err := grpc.StartServer(*addr, NewRadiance()); err != nil {
		panic(err)
	}
}

// The gRPC server prefers the context-aware rich path for these methods.
var _ grpc.AIModelRichContext = (*Radiance)(nil)
