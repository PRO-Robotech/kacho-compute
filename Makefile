BINARY         := kacho-compute
CMD            := ./cmd/compute
IMAGE          := kacho-compute:dev

.PHONY: build test test-short vet lint docker sync-migrations audit-list-filter

build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) $(CMD)

test:
	go test ./... -race -cover -timeout 300s

test-short:
	go test ./... -race -cover -short -timeout 120s

vet:
	go vet ./...

# audit-list-filter — RBAC v2 / KAC-219 / W6 CI gate.
# Refuses to ship a public List<Resource> handler without authzfilter wiring.
# Whitelist admin-only / catalog handlers via --allow=<HandlerName>.
audit-list-filter:
	@./tools/audit-list-filter.sh

lint:
	golangci-lint run ./...

sync-migrations:
	cp ../kacho-corelib/migrations/common/*.sql migrations/

docker:
	cd .. && docker build -f kacho-compute/Dockerfile -t $(IMAGE) .

.PHONY: migrate-up migrate-down migrate-status
migrate-up:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate up

migrate-down:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate down

migrate-status:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate status
