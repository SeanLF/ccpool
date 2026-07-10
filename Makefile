.PHONY: fmt fmt-check vet staticcheck vuln test build check demo

# ccpool is a single static Go binary. Dev tools (gofumpt/staticcheck/govulncheck) are pinned via
# `tool` directives in go.mod, so `go tool <x>` is reproducible with no global installs.

fmt: ## format Go sources in place
	go tool gofumpt -w .

fmt-check: ## fail if any Go source is unformatted
	@out="$$(go tool gofumpt -l .)"; if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

vet: ## go vet
	go vet ./...

staticcheck: ## staticcheck lint
	go tool staticcheck ./...

vuln: ## govulncheck (dependency CVE scan)
	go tool govulncheck ./...

test: ## run the test suite (conformance runs against committed goldens; no Ruby needed)
	go test ./...

build: ## build the static binary
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ccpool .

check: fmt-check vet staticcheck vuln test ## the full pre-commit gate

demo: ## regenerate the demo GIFs (needs vhs: `brew install vhs`)
	vhs demo/overview.tape
	vhs demo/init.tape
