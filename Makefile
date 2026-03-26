.PHONY: test build docker-build docker-up docker-down lint

test:
	go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dsforms .

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

lint:
	go vet ./...
