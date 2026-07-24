.PHONY: test test-go test-python test-dashboard run mock dashboard build docker-up docker-down

GOCACHE ?= /tmp/outcome-router-go-cache
GOMODCACHE ?= /tmp/outcome-router-go-mod

test: test-go test-python test-dashboard

test-go:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...

test-python:
	cd python && PYTHONPATH=src python3 -m unittest discover -s tests -v

test-dashboard:
	cd dashboard && npm test

run:
	OUTCOME_ROUTER_CONFIG=config/demo.json go run ./cmd/router

mock:
	go run ./cmd/mock-provider

dashboard:
	cd dashboard && npm run dev

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) CGO_ENABLED=0 go build -trimpath -o bin/outcome-router ./cmd/router
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) CGO_ENABLED=0 go build -trimpath -o bin/mock-provider ./cmd/mock-provider

docker-up:
	docker compose up --build

docker-down:
	docker compose down
