.PHONY: test lint check demo

test: ## run the hermetic test suite
	ruby test_ccpool.rb

lint: ## run rubocop
	rubocop

check: test lint ## the full pre-commit gate

demo: ## regenerate the demo GIFs (needs vhs: `brew install vhs`)
	vhs demo/overview.tape
	vhs demo/init.tape
