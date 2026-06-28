// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestKAH1_NoBareGRPCDialWithoutKeepalive — анти-регресс-страж keepalive на dial.
//
// Любой inter-service `grpc.NewClient(...)` / `grpc.Dial(...)` в этом пакете обязан
// нести keepalive (через grpcclient.KeepaliveDialOption/KeepaliveParams). Без него
// idle-conn между всплесками трафика становится half-open, и первый RPC всплеска
// висит ~30с на переустановке. Новый bare-dial
// без keepalive должен ронять этот тест ДО мержа.
//
// RED-демонстрация: убрать grpcclient.KeepaliveDialOption из peerDialOpts → тест падает.
func TestKAH1_NoBareGRPCDialWithoutKeepalive(t *testing.T) {
	const keepaliveMarker = "grpcclient.Keepalive" // KeepaliveDialOption | KeepaliveParams
	dialRe := regexp.MustCompile(`grpc\.(NewClient|Dial)\(`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(b)
		if !dialRe.MatchString(src) {
			continue
		}
		checked++
		if !strings.Contains(src, keepaliveMarker) && !strings.Contains(src, "WithKeepaliveParams") {
			t.Errorf("%s: содержит grpc.NewClient/Dial без keepalive (KAC-244 KA-H1). "+
				"Inter-service dial обязан использовать grpcclient.KeepaliveDialOption(idle).", name)
		}
	}
	if checked == 0 {
		t.Fatal("ни одного grpc.NewClient/Dial не найдено — страж потерял цель (проверь, не переехал ли dial)")
	}
}
