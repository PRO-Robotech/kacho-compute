# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

BINARY         := kacho-compute
CMD            := ./cmd/compute
IMAGE          := kacho-compute:dev

.PHONY: build test test-short vet lint docker sync-migrations audit-list-filter proto-install-plugins proto-lint proto-vendor proto-gen

build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) $(CMD)
	CGO_ENABLED=0 go build -o bin/kacho-migrator ./cmd/migrator

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

# proto-vendor — подтягивает corelib-owned инфра-протосы
# (operation/validation/authz_options/cloud-api + vendored google) из ../kacho-corelib/proto
# в proto/ для buf-резолва импортов. Единственный источник истины этих файлов —
# kacho-corelib; здесь они gitignored и в git не коммитятся (Go-stubs тоже single-source
# corelib). Запускается перед proto-gen.
proto-vendor:
	@set -e; src=../kacho-corelib/proto; \
	for f in google/api/annotations.proto google/api/field_behavior.proto \
	         google/api/http.proto google/rpc/status.proto \
	         kacho/cloud/api/operation.proto kacho/cloud/operation/operation.proto \
	         kacho/cloud/validation.proto kacho/iam/authz/v1/authz_options.proto; do \
	  mkdir -p "proto/$$(dirname $$f)"; \
	  cp "$$src/$$f" "proto/$$f"; \
	done

# proto-gen — регенерация Go-stubs доменного proto compute (kacho/cloud/compute/v1,
# kacho/cloud/access, kacho/cloud/maintenance/v2) из proto/. Универсальная ИНФРА
# (operation/validation/authz_options/cloud-api/google) подтягивается proto-vendor только
# для buf-резолва импортов и НЕ генерируется (Go-stubs живут в kacho-corelib / canonical
# genproto) — см. proto/buf.gen.yaml inputs.paths.
proto-gen: proto-vendor
	cd proto && buf generate

.PHONY: migrate-up migrate-down migrate-status
migrate-up:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/kacho-migrator up

migrate-down:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/kacho-migrator down

migrate-status:
	KACHO_COMPUTE_DB_PASSWORD=secret bin/kacho-migrator status
