package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKVServeRequiresBothTLSFlagsTogether(t *testing.T) {
	file := filepath.Join(t.TempDir(), "kv.json")
	for _, args := range [][]string{
		{"-file", file, "-tls-cert", "cert.pem"},
		{"-file", file, "-tls-key", "key.pem"},
	} {
		err := runKVServe(args)
		if err == nil {
			t.Fatalf("runKVServe(%v): expected an error for a lone --tls-cert/--tls-key", args)
		}
		if !strings.Contains(err.Error(), "must be set together") {
			t.Errorf("runKVServe(%v) = %v, want a \"must be set together\" error", args, err)
		}
	}
}
