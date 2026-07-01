package dialer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	handshakeTimeout = 15 * time.Second
)

// IPProvider is an interface for providing preferred CF IPs at runtime.
type IPProvider interface {
	GetIP() string
}

// Dialer is a TLS dialer with preferred-IP support.
type Dialer struct {
	Port string

	// ipProvider is called each time a connection is needed to get a fresh preferred IP.
	// When set, the IP it returns is used as the dial target (SNI remains the domain).
	ipProvider IPProvider

	// ponytail: stage timing for test comparison, set on each DialTLSContext call
	LastDial time.Duration // DNS + TCP connect
	LastTLS  time.Duration // TLS handshake
}

// New creates a new dialer.
func New() *Dialer {
	return &Dialer{
		Port: "443",
	}
}

// SetIPProvider sets a runtime IP source for preferred CF edge IPs.
func (d *Dialer) SetIPProvider(p IPProvider) {
	d.ipProvider = p
}

// DialTLSContext establishes a TLS connection to domain.
// If ipProvider is set, dials the IP it provides while keeping SNI as domain.
func (d *Dialer) DialTLSContext(ctx context.Context, network, domain string) (net.Conn, error) {
	var ipSource string
	addr := domain + ":" + d.Port
	if p := d.getPreferredIP(); p != "" {
		addr = p + ":" + d.Port
		ipSource = "ipdb"
	} else {
		ipSource = "dns"
	}
	log.Printf("[dialer] dial domain=%s addr=%s source=%s", domain, addr, ipSource)

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: domain,
	}

	dialStart := time.Now()
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	d.LastDial = time.Since(dialStart)

	tlsStart := time.Now()
	tlsConn := tls.Client(rawConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("tls handshake to %s: %w", addr, err)
	}
	d.LastTLS = time.Since(tlsStart)

	return tlsConn, nil
}

func (d *Dialer) getPreferredIP() string {
	if d.ipProvider == nil {
		return ""
	}
	return d.ipProvider.GetIP()
}
