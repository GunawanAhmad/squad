export GOFLAGS := -buildvcs=false

# Not ./...: frontend/node_modules contains stray Go packages (flatted).
GO_PKGS := $(shell go list ./... 2>/dev/null | grep -v node_modules)

.PHONY: dev ui web lint build test search eval down

# Start SearXNG, then run the app on :8080, serving the built UI from web/.
# Go or characters.json changes need Ctrl+C and `make dev` again.
dev: .env search build
	DEV=1 exec ./characters # exec, not `go run`: no middle process that can die and leave the server orphaned

# UI with hot reload on :5173 (run alongside `make dev`; /api is proxied to it).
ui: frontend/node_modules
	cd frontend && npm run dev

# Build the Vue app into web/, which the Go binary embeds.
web: frontend/node_modules
	cd frontend && npm run build

lint: frontend/node_modules
	cd frontend && npm run lint

frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci && touch node_modules

build: web
	go build -o characters .

test: web lint
	go vet $(GO_PKGS)
	go test $(GO_PKGS) # not ./...: frontend/node_modules contains stray Go packages

# Model evals (docs/assistant-roadmap.md §7). Spends real API credit:
#   make eval models=deepseek-v4.1-flash,claude-haiku-5-5 runs=3
# `go run ./eval -dry-run -models=...` prints the call estimate first.
eval:
	go run ./eval $(if $(models),-models=$(models),) $(if $(judge),-judge=$(judge),) $(if $(runs),-runs=$(runs),) $(if $(sets),-sets=$(sets),)

search:
	docker compose up -d --wait

down:
	docker compose down

.env:
	cp .env.example .env
	@echo "Created .env. Add your OPENCODE_API_KEY, then run make dev again." && false
