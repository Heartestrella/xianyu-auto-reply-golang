.PHONY: test frontend build run docker
test:
	go test ./...
frontend:
	cd frontend && npm ci && npm run build
build: frontend
	go build -trimpath -o bin/xianyu-go ./cmd/server
run:
	go run ./cmd/server
docker:
	docker compose up -d --build
