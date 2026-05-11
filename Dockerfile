FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY kacho-corelib /src/kacho-corelib
COPY kacho-proto /src/kacho-proto
COPY kacho-compute /src/kacho-compute

WORKDIR /src/kacho-compute
RUN go mod download
RUN CGO_ENABLED=0 go build -o /kacho-compute ./cmd/compute

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /kacho-compute /usr/local/bin/kacho-compute
USER 65532
ENTRYPOINT ["/usr/local/bin/kacho-compute"]
