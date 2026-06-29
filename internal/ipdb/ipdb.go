package ipdb

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// IPDB API address for best Cloudflare IPs.
	apiURL = "https://ipdb.api.030101.xyz/?type=bestcf"

	// Refresh interval: pull new IPs every 60 minutes.
	refreshInterval = 60 * time.Minute

	// HTTP timeout for fetching IP list.
	fetchTimeout = 15 * time.Second
)

// Client fetches and caches Cloudflare preferred IPs from IPDB.
type Client struct {
	mu     sync.RWMutex
	ips    []string
	stopCh chan struct{}
	once   sync.Once
}

// NewClient creates an IPDB client and starts periodic refresh.
func NewClient() *Client {
	c := &Client{
		stopCh: make(chan struct{}),
	}
	c.startRefreshLoop()
	return c
}

// GetIP returns a random preferred IP from the cache.
// Returns empty string if no IPs are available yet.
func (c *Client) GetIP() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.ips) == 0 {
		return ""
	}
	return c.ips[rand.Intn(len(c.ips))]
}

// GetAllIPs returns a copy of the current IP list.
func (c *Client) GetAllIPs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]string, len(c.ips))
	copy(out, c.ips)
	return out
}

// Count returns the number of cached IPs.
func (c *Client) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.ips)
}

// RefreshNow immediately fetches the latest IP list.
func (c *Client) RefreshNow() error {
	ips, err := fetchIPs()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.ips = ips
	c.mu.Unlock()

	log.Printf("[ipdb] refreshed count=%d", len(ips))
	return nil
}

// Stop shuts down the periodic refresh loop.
func (c *Client) Stop() {
	c.once.Do(func() {
		close(c.stopCh)
	})
}

func (c *Client) startRefreshLoop() {
	// Do an initial fetch immediately.
	if err := c.RefreshNow(); err != nil {
		log.Printf("[ipdb] initial_fetch_failed err=%v — retrying in background", err)
	}

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-ticker.C:
				if err := c.RefreshNow(); err != nil {
					log.Printf("[ipdb] periodic_refresh_failed err=%v", err)
				}
			}
		}
	}()
}

// fetchIPs calls the IPDB API and returns the list of preferred CF IPs.
func fetchIPs() ([]string, error) {
	client := &http.Client{Timeout: fetchTimeout}

	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("ipdb request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ipdb status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read ipdb body: %w", err)
	}

	lines := strings.Split(string(body), "\n")
	var ips []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Handle "IP#country" format
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ip := net.ParseIP(line)
		if ip == nil {
			log.Printf("[ipdb] skip_invalid_line value=%q", line)
			continue
		}
		ips = append(ips, line)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("ipdb returned empty IP list")
	}

	return ips, nil
}
