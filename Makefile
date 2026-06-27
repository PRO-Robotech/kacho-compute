BINARY         := kacho-compute
CMD            := ./cmd/compute
IMAGE          := kacho-compute:dev

.PHONY: build test test-short vet lint docker sync-migrations audit-list-filter proto-install-plugins proto-lint proto-gen

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

# proto-install-plugins — ставит protoc-плагины в $GOBIN (lookup через $PATH для buf).
# Доменный proto compute генерируется этими тремя плагинами; permission-catalog для compute —
# hand-written (internal/check/permission_map.go), buf-catalog-плагин не нужен.
proto-install-plugins:
	go install google.golang.org/protobuf/cmd/protoc-gen-go
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway

proto-lint:
	cd proto && buf lint

# proto-gen — регенерация Go-stubs доменного proto compute (kacho/cloud/compute/v1,
# kacho/cloud/access, kacho/cloud/maintenance/v2) из proto/. Универсальная ИНФРА
# (operation/validation/authz_options/cloud-api/google) вендорится в proto/ только для
# buf-резолва импортов и НЕ генерируется (Go-stubs живут в kacho-corelib / canonical
# genproto) — см. proto/buf.gen.yaml inputs.paths.
proto-gen:
	cd proto && buf generate

.PHONY: migrate-up migrate-down migrate-status
migrate-up:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate up

migrate-down:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate down

migrate-status:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/$(BINARY) migrate status
