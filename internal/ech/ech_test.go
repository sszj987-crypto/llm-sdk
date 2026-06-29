package ech

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRetrieveECHConfig_SSZJ(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	config, err := retrieveECHConfig(ctx, "sszj.me")
	if err != nil {
		t.Fatalf("DoH retrieval failed: %v", err)
	}
	if config == nil {
		t.Fatal("no ECH config returned from sszj.me")
	}
	fmt.Printf("ECH config for sszj.me: %d bytes\n", len(config))
}

func TestDialTLSWithECH_SSZJ(t *testing.T) {
	d := NewDialer()
	d.SetEnableECH(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := d.DialTLSContext(ctx, "tcp", "sszj.me")
	if err != nil {
		t.Fatalf("ECH dial to sszj.me failed: %v", err)
	}
	conn.Close()
	t.Log("ECH dial to sszj.me succeeded!")
}
