FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Single-repo build: зависимости (kacho-corelib, kacho-iam, kacho-geo, kacho-vpc) —
# versioned-модули с GitHub (go.mod без replace), build-context — этот репо.
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /kacho-compute ./cmd/compute

FROM mirror.gcr.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /kacho-compute /usr/local/bin/kacho-compute
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-compute"]
