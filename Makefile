.PHONY: build build-go build-tap clean

build: build-go build-tap

build-go:
	mise exec -- go build -o bin/nastro .

build-tap:
	/usr/bin/swiftc -O -framework CoreAudio -framework AVFoundation -framework AudioToolbox \
		-o bin/nastro-tap tap/main.swift

clean:
	rm -rf bin
