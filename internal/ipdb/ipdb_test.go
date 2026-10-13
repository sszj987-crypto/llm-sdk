package ipdb

import (
	"os"
	"testing"
)

func TestFetchIPs(t *testing.T) {
	if os.Getenv("LLM_RUN_LIVE_TESTS") != "1" {
		t.Skip("set LLM_RUN_LIVE_TESTS=1 to fetch live IPDB data")
	}
	ips, err := fetchIPs()
	if err != nil {
		t.Fatalf("fetchIPs failed: %v", err)
	}
	t.Logf("fetched %d IPs, first 5: %v", len(ips), ips[:min(5, len(ips))])
}

func TestClient(t *testing.T) {
	c := &Client{
		ips:    []string{"1.1.1.1", "1.0.0.1"},
		stopCh: make(chan struct{}),
	}

	if c.Count() == 0 {
		t.Fatal("expected at least one IP after init")
	}

	ip := c.GetIP()
	t.Logf("random IP: %s", ip)

	all := c.GetAllIPs()
	t.Logf("total IPs: %d", len(all))
}
