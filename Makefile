export GOFLAGS := -buildvcs=false

# Not ./...: frontend/node_modules contains stray Go packages (flatted).
GO_PKGS := $(shell go list ./... 2>/dev/null | grep -v node_modules)

.PHONY: dev ui web lint build test search eval down

# Everything with hot reload on one URL: compose services, Vite with HMR, and
# the Go server rebuilt and restarted when Go/SQL/characters.json/.env change.
# Ctrl+C stops the app and Vite; the compose services stay up for the next run.
dev: .env search frontend/node_modules
	@mkdir -p .tmp && go build -o .tmp/dev ./dev && exec .tmp/dev # exec: one process to signal, no `go run` wrapper

# Vite alone on :5173 (proxies /api to a running server); `make dev` runs it.
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
	go build -o squad .

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
