.PHONY: build build-go build-tap check-tap clean install

# Plain `go` when it's on PATH (CI, Homebrew, contributors); mise fallback
# for dev machines that manage Go through mise only.
GO := $(shell command -v go >/dev/null 2>&1 && echo go || echo "mise exec -- go")
PREFIX ?= /usr/local

build: build-go build-tap

build-go:
	$(GO) build -o bin/nastro .

build-tap:
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
