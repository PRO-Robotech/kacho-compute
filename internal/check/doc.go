// Package check содержит kacho-compute per-service Check-interceptor wiring
// под E3 / KAC-108 (см. acceptance §6 D4).
//
// Состав:
//   - permission_map.go   — RPCMap для всех публичных RPC kacho-compute
//     (Disk, Image, Snapshot, Instance, DiskType, Zone, Region + Operation).
//     Reference/SetAccessBindings RPC опущены: их аутентификация делается
//     самой kacho-iam, не повторяем здесь (см. acceptance §4 «Cascade /
//     Computed Relation»). access-bindings handler-ы в compute сейчас —
//     no-op скелет, см. CLAUDE.md §1.
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
// этом MVP. TTL=5s + outbox-drain≤2s = ≤10s revoke propagation (KAC-104 DoD #5).
package check
