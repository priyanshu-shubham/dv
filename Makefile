.PHONY: build frontend go install watch test clean

# Full build: bundle the UI, then compile the Go binary with it embedded.
build: frontend go

frontend:
	cd web/frontend && npm install --no-audit --no-fund && npm run build

go:
	go build -o dv .

# Install to ~/.local/bin so `dv` works from any repository.
install: build
	mkdir -p $(HOME)/.local/bin
	install -m 0755 dv $(HOME)/.local/bin/dv

# Rebuild the bundle on save; run `go run . -no-open` alongside it.
watch:
	cd web/frontend && npm run watch

test:
	go test ./...

clean:
	rm -f dv
	rm -f internal/server/static/bundle.js internal/server/static/bundle.css
	rm -f internal/server/static/chunk-*.js
