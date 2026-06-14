FROM --platform=$BUILDPLATFORM mirror.gcr.io/library/golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY kacho-corelib /src/kacho-corelib
COPY kacho-proto /src/kacho-proto
COPY kacho-compute /src/kacho-compute

WORKDIR /src/kacho-compute
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /kacho-compute ./cmd/compute

FROM mirror.gcr.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /kacho-compute /usr/local/bin/kacho-compute
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-compute"]
