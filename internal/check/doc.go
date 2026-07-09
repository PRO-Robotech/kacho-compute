// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package check содержит kacho-compute per-service Check-interceptor wiring.
//
// Состав:
//   - permission_map.go   — RPCMap для всех публичных RPC kacho-compute
//     (Disk, Image, Snapshot, Instance, DiskType + Operation). Region/Zone
//     serving снят — Geography принадлежит kacho-geo (миграция 0011).
//     Reference/SetAccessBindings RPC опущены: их аутентификация делается
//     самой kacho-iam, не повторяем здесь. access-bindings handler-ы в compute
//     сейчас — no-op скелет.
//   - check_client.go     — gRPC adapter поверх `iamv1.InternalIAMServiceClient.Check`.
//   - factory.go          — фабрика, собирающая `*authz.Interceptor` из
//     (IAMConn, Breakglass). nil-conn + Breakglass=false → ErrIAMConnNotConfigured
//     (graceful start без kacho-iam в dev).
//
// Wiring (composition root — `cmd/compute/main.go`):
//
//	authzIntr, err := check.NewInterceptor(check.Options{
//	    ServiceName: "kacho-compute",
//	    IAMConn:     iamConn,        // *grpc.ClientConn к kacho-iam:9091
//	    Breakglass:  cfg.AuthZBreakglass,
//	    Logger:      logger,
//	})
//	if err != nil { return err }
//	if authzIntr != nil {
//	    publicUnary = append(publicUnary, authzIntr.Unary())
//	    publicStream = append(publicStream, authzIntr.Stream())
//	}
//
// Cache-invalidation (LISTEN/NOTIFY → `kacho_iam_subjects`) — НЕ wired в
// этом MVP. TTL=5s + outbox-drain≤2s = ≤10s revoke propagation.
package check
