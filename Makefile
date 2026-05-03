BINARY := compute
CMD     := ./cmd/compute
IMAGE   := kacho-compute:dev

.PHONY: build test vet lint docker sync-migrations generate

build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) $(CMD)

test:
	go test ./... -race -cover -timeout 300s

vet:
	go vet ./...

lint:
	golangci-lint run ./... || true

generate:
	/home/dk/go/bin/sqlc generate

sync-migrations:
	cp ../kacho-corelib/migrations/common/*.sql migrations/
	cp ../kacho-corelib/migrations/common/*.sql internal/migrations/

docker:
	cd .. && docker build -f kacho-compute/Dockerfile -t $(IMAGE) .

.PHONY: migrate-up migrate-down migrate-status
migrate-up:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate up

migrate-down:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate down

migrate-status:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate status
