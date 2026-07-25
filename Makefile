.PHONY: build install test web

build:
	go build ./cmd/midgard

install:
	go install ./cmd/midgard

test:
	go test ./...
	cd web && ./node_modules/.bin/tsc --noEmit
	cd web && ./node_modules/.bin/vitest run

web:
	cd web && ./node_modules/.bin/tsc --noEmit
	cd web && ./node_modules/.bin/vite build
	rsync -a --delete web/dist/ internal/webassets/dist/
