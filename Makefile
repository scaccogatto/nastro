.PHONY: build build-go build-tap check-tap clean install

# Plain `go` when it's on PATH (CI, Homebrew, contributors); mise fallback
# for dev machines that manage Go through mise only.
GO := $(shell command -v go >/dev/null 2>&1 && echo go || echo "mise exec -- go")
PREFIX ?= /usr/local
# Shown by `nastro version`. From git when building a checkout, overridable
# (`make VERSION=1.2.3`) for builds from a release tarball, which has no .git.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build: build-go build-tap

# Order-only prerequisite on bin/: Homebrew builds with a parallel make, and
# build-tap otherwise races build-go for the directory, failing with
# "ld: open() failed, errno=2 ... for bin/nastro-tap".
bin:
	mkdir -p bin

build-go: | bin
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/nastro .

build-tap: | bin
	/usr/bin/swiftc -O -framework CoreAudio -framework AVFoundation -framework AudioToolbox \
		-framework AppKit -framework CoreGraphics \
		-o bin/nastro-tap tap/main.swift

check-tap: build-tap
	bin/nastro-tap --self-check

install: build
	install -d $(PREFIX)/bin
	install bin/nastro bin/nastro-tap $(PREFIX)/bin

clean:
	rm -rf bin
