.PHONY: fmt fmt-check vet staticcheck vuln test build check demo

# ccpool is a single static Go binary. Dev tools (gofumpt/staticcheck/govulncheck) are pinned via
# `tool` directives in go.mod, so `go tool <x>` is reproducible with no global installs.

# Let gofumpt do its own walking. Handing it a `git ls-files` list instead was tried and reverted:
# xargs/make word-splitting silently DROPS any path with a space or a non-ASCII (C-quoted) name, so
# the gate would pass having never looked at that file, and it made `make check` require a git work
# tree (a source tarball could no longer run the gate). Walking `.` is filename-safe and skips
# generated files on its own. The cost is that an unformatted throwaway under the gitignored
# scratch/ fails the gate; `make fmt` fixes that in one command.
fmt: ## format Go sources in place
	go tool gofumpt -w .

# `|| exit` is load-bearing: gofumpt exits non-zero with EMPTY stdout when a file won't parse, so
# testing only for non-empty output let an unparseable source pass the gate silently.
fmt-check: ## fail if any Go source is unformatted
	@out="$$(go tool gofumpt -l .)" || { echo "gofumpt failed"; exit 1; }; \
	if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

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
