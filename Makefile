export GOFLAGS := -buildvcs=false

.PHONY: dev build test search down

# Start SearXNG, then run the app. HTML is served from disk (refresh to see edits);
# Go or characters.json changes need Ctrl+C and `make dev` again.
dev: .env search build
	DEV=1 exec ./characters # exec, not `go run`: no middle process that can die and leave the server orphaned

build:
	go build -o characters .

test:
	go vet ./... && go test ./...

search:
	docker compose up -d --wait

down:
	docker compose down

.env:
	cp .env.example .env
	@echo "Created .env. Add your OPENCODE_API_KEY, then run make dev again." && false
