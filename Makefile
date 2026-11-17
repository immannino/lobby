.PHONY: run dev test lint bench

run:
	go run ./examples/site

# Live reload (requires github.com/air-verse/air)
dev:
	air

test:
	go test -race ./...

lint:
	go vet ./...

bench:
	go test -run '^$$' -bench . -benchmem .
