.PHONY: all frontend server test

all: frontend server

frontend:
	cd frontend && npm run build
	mkdir -p internal/static/dist
	cp frontend/dist/* internal/static/dist/

server:
	go build -o agentbridge ./cmd/agentbridge

test:
	go test ./...
