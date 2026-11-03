package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReachableAccessPointSkipsPrivateAndInvalid(t *testing.T) {
	if got := reachableAccessPoint([]string{"10.0.0.5:443", "127.0.0.1:443", "bad", "a,b:1"}); got != "" {
		t.Fatalf("unexpected access point %q", got)
	}
}

func TestLoginServerForKnownServerUsesName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bases", "Broker-Live1"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &worker{}
	if got := w.loginServerFor(dir, "Broker-Live1"); got != "Broker-Live1" {
		t.Fatalf("got %q", got)
	}
}
