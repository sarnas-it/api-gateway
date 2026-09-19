BINARY   ?= api-gateway
BIN_DIR  ?= bin
CONFIG   ?= /etc/proxy/config.yaml

DASHBOARD_BINARY          ?= api-gateway-dashboard
DASHBOARD_CONFIG          ?= $(CONFIG)
DASHBOARD_DISCOVERY_STATE ?=
DASHBOARD_METRICS_URL     ?= http://127.0.0.1:8080/metrics
DASHBOARD_LISTEN          ?= 127.0.0.1:8081
DASHBOARD_REFRESH         ?= 5s

GO       ?= go
GOLANGCI ?= golangci-lint

.PHONY: all build run check test lint coverage clean docker-build fmt vet dashboard dashboard-run plugin-build plugin-so-build

all: fmt vet lint build test

build:
	$(GO) build -ldflags="-w -s" -trimpath -o $(BIN_DIR)/$(BINARY) ./cmd/

plugin-build:
	$(GO) build -o $(BIN_DIR)/plugins/jwt ./cmd/plugins/jwt/
	$(GO) build -o $(BIN_DIR)/plugins/ratelimit ./cmd/plugins/ratelimit/
	$(GO) build -o $(BIN_DIR)/plugins/events ./cmd/plugins/events/
	$(GO) build -o $(BIN_DIR)/plugins/discovery ./cmd/plugins/discovery/

plugin-so-build:
	$(GO) build -buildmode=plugin -trimpath -tags pluginmain -o $(BIN_DIR)/plugins/jwt.so ./cmd/plugins/jwt/

run: build
	./$(BIN_DIR)/$(BINARY) -config $(CONFIG)

check: build
	./$(BIN_DIR)/$(BINARY) -config $(CONFIG) -check

dashboard:
	$(GO) build -buildvcs=false -ldflags="-w -s" -trimpath -o $(BIN_DIR)/$(DASHBOARD_BINARY) ./cmd/dashboard

dashboard-run: dashboard
	./$(BIN_DIR)/$(DASHBOARD_BINARY) \
		-config $(DASHBOARD_CONFIG) \
		-discovery-state "$(DASHBOARD_DISCOVERY_STATE)" \
		-metrics-url $(DASHBOARD_METRICS_URL) \
		-listen $(DASHBOARD_LISTEN) \
		-refresh $(DASHBOARD_REFRESH)

test:
	$(GO) test -v -race -count=1 -coverprofile=$(BIN_DIR)/coverage.out ./...
	$(GO) tool cover -func=$(BIN_DIR)/coverage.out

lint:
	$(GOLANGCI) run ./...

coverage: test
	$(GO) tool cover -html=$(BIN_DIR)/coverage.out -o $(BIN_DIR)/coverage.html

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BIN_DIR)

docker-build:
	docker build -t $(BINARY) .

docker-buildx:
	docker buildx build --platform linux/amd64 -t $(BINARY) .

lint-ci:
	golangci-lint run ./... --output.json.path=lint-report.json
	scripts/lint-to-issues.sh
	test ! -s lint-report.json

install-hooks:
	cp .githooks/pre-push .git/hooks/pre-push
	chmod +x .git/hooks/pre-push
	@echo "✅ Pre-push hook installed"

gen-proto:
	protoc -I proto/features --go_out=. --go_opt=module=github.com/basili4-1982/api-gateway \
		--go-grpc_out=. --go-grpc_opt=module=github.com/basili4-1982/api-gateway \
		proto/features/authsvc.proto proto/features/ratelimit.proto \
		proto/features/events.proto proto/features/discovery.proto
