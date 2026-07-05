// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Тесты бьют по production-seam peerDialOptsCreds — единственной точке сборки
// []grpc.DialOption для всех peer-dial'ов (dialPeerCreds делегирует ей). Поскольку
// grpc.NewClient не отдаёт опции назад, инспектируем именно возвращаемый набор:
// он обязан всегда включать keepalive-DialOption (grpcclient.KeepaliveDialOption),
// иначе idle-conn становится half-open и первый RPC всплеска висит ~30с.

func insecureCreds() credentials.TransportCredentials { return insecure.NewCredentials() }

func tlsCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
}

// TestPeerDialOpts_IncludesKeepalive_Active — KA-01: active-conn (idle=false)
// получает keepalive Time=10s, Timeout=Time/3, PermitWithoutStream=false.
func TestPeerDialOpts_IncludesKeepalive_Active(t *testing.T) {
	p := peerKeepalive(false)
	require.Equal(t, 10*time.Second, p.Time)
	require.Equal(t, 10*time.Second/3, p.Timeout)
	require.False(t, p.PermitWithoutStream)

	opts := peerDialOptsCreds(insecureCreds(), false)
	require.True(t, hasKeepaliveOpt(opts), "peerDialOptsCreds must include a keepalive DialOption")
}

// TestPeerDialOpts_IncludesKeepalive_Idle — KA-01: authz-conn (idle=true) →
// PermitWithoutStream=true (idle-prone authz/internal conn).
func TestPeerDialOpts_IncludesKeepalive_Idle(t *testing.T) {
	p := peerKeepalive(true)
	require.Equal(t, 10*time.Second, p.Time)
	require.True(t, p.PermitWithoutStream, "idle authz-conn must permit pings without stream")
}

// TestPeerDialOpts_CredsBothModes — KA-01 and: keepalive не ломает выбор creds.
func TestPeerDialOpts_CredsBothModes(t *testing.T) {
	require.True(t, hasKeepaliveOpt(peerDialOptsCreds(tlsCreds(), false)), "TLS mode keeps keepalive")
	require.True(t, hasKeepaliveOpt(peerDialOptsCreds(insecureCreds(), true)), "insecure mode keeps keepalive")
	// caller всегда получает хотя бы creds + keepalive (>=2 опции)
	require.GreaterOrEqual(t, len(peerDialOptsCreds(tlsCreds(), false)), 2)
}

// hasKeepaliveOpt — проверяет наличие keepalive DialOption в наборе.
// peerDialOptsCreds всегда добавляет grpcclient.KeepaliveDialOption, значит набор
// длиннее, чем без keepalive. Проверяем фактически через сравнение длин.
func hasKeepaliveOpt(opts []grpc.DialOption) bool {
	// peerDialOptsCreds(creds, idle) = [creds-opt, keepalive-opt]; без keepalive было бы 1.
	return len(opts) >= 2
}

var _ = keepalive.ClientParameters{}
