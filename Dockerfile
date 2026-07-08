FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Single-repo build: зависимости (kacho-corelib, kacho-iam, kacho-geo, kacho-vpc) —
# versioned-модули с GitHub (go.mod без replace), build-context — этот репо.
COPY . .
RUN go mod download
# Два независимых binary в одном образе:
# kacho-compute  — gRPC API-сервер (только `serve`; не несёт миграции).
# kacho-migrator — CLI миграций ({up|down|status}), используется init-container'ом
# перед стартом основного pod'а (least-privilege: serve-образ не может менять схему).
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /kacho-compute ./cmd/compute \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /kacho-migrator ./cmd/migrator

FROM mirror.gcr.io/library/alpine:3.20
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates
COPY --from=builder /kacho-compute /usr/local/bin/kacho-compute
COPY --from=builder /kacho-migrator /usr/local/bin/kacho-migrator
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-compute"]
