.PHONY: test lint check demo \
	go-fmt go-fmt-check go-vet go-staticcheck go-vuln go-test go-build go-check

# --- Go (the v1 target; hot path first, see docs/GO-MIGRATION.md) ---
# Tools are pinned via `tool` directives in go.mod, so `go tool <x>` is reproducible with no
# global installs.

go-fmt: ## format Go sources in place
	go tool gofumpt -w .

go-fmt-check: ## fail if any Go source is unformatted
	@out="$$(go tool gofumpt -l .)"; if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

go-vet: ## go vet
	go vet ./...

go-staticcheck: ## staticcheck lint (the rubocop analog)
	go tool staticcheck ./...

go-vuln: ## govulncheck (the CodeQL analog for deps)
	go tool govulncheck ./...

go-test: ## run the Go test suite
	go test ./...

go-build: ## build the static binary
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ccpool .

go-check: go-fmt-check go-vet go-staticcheck go-vuln go-test ## full Go gate

# --- Ruby (reference + conformance oracle until Ruby is retired at v1) ---

test: ## run the hermetic Ruby test suite
	ruby test_ccpool.rb

lint: ## run rubocop
	rubocop

check: test lint go-check ## the full pre-commit gate (both languages during migration)

demo: ## regenerate the demo GIFs (needs vhs: `brew install vhs`)
	vhs demo/overview.tape
	vhs demo/init.tape
