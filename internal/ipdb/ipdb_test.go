package ipdb

import (
	"testing"
)

func TestFetchIPs(t *testing.T) {
	ips, err := fetchIPs()
	if err != nil {
		t.Fatalf("fetchIPs failed: %v", err)
	}
	t.Logf("fetched %d IPs, first 5: %v", len(ips), ips[:min(5, len(ips))])
}

func TestClient(t *testing.T) {
	c := NewClient()
	defer c.Stop()

	if c.Count() == 0 {
		t.Fatal("expected at least one IP after init")
	}

	ip := c.GetIP()
	t.Logf("random IP: %s", ip)

	all := c.GetAllIPs()
	t.Logf("total IPs: %d", len(all))
}
