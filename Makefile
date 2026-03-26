.PHONY: test build docker-build docker-up docker-down dev-up dev-down lint

test:
	go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o dsforms .

# Production
docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

# Development (with Mailpit for email testing)
dev-up:
	docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d

dev-down:
	docker compose -f docker-compose.yml -f docker-compose.dev.yml down

lint:
	go vet ./...
