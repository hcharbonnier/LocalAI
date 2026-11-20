GOCMD=go

# Packaged as a gallery backend by backend/Dockerfile.golang. The Go program is
# the gRPC shim; the radiance engine itself is built by the engine build unit
# and staged next to it, then copied by package.sh.
radiance:
	CGO_ENABLED=0 $(GOCMD) build -ldflags "$(LD_FLAGS)" -tags "$(GO_TAGS)" -o radiance ./

test:
	CGO_ENABLED=0 $(GOCMD) test ./...

package:
	bash package.sh

build: radiance package

clean:
	rm -rf radiance package
