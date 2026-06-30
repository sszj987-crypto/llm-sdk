package dialer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"

	"github.com/quic-go/quic-go"
)

// QuicDialer dials using QUIC (UDP) with optional preferred-IP support via IPDB.
type QuicDialer struct {
	ipProvider IPProvider
}

// NewQuicDialer creates a new QUIC dialer.
func NewQuicDialer() *QuicDialer {
	return &QuicDialer{}
}

// SetIPProvider attaches a runtime IP source for preferred edge IPs.
func (d *QuicDialer) SetIPProvider(p IPProvider) {
	d.ipProvider = p
}

// Dial creates a QUIC connection for use with http3.RoundTripper.
// When an IPProvider is set, dials the preferred IP and keeps SNI as the original host.
func (d *QuicDialer) Dial(ctx context.Context, addr string, tlsCfg *tls.Config, quicCfg *quic.Config) (*quic.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("quic split host port %q: %w", addr, err)
	}

	var ipSource string
	if p := d.getPreferredIP(); p != "" {
		host = p
		ipSource = "ipdb"
	} else {
		ipSource = "dns"
	}

	target := net.JoinHostPort(host, port)
	log.Printf("[quic_dialer] dial addr=%s target=%s source=%s", addr, target, ipSource)

	udpAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, fmt.Errorf("quic resolve %s: %w", target, err)
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("quic listen udp: %w", err)
	}

	conn, err := quic.Dial(ctx, udpConn, udpAddr, tlsCfg, quicCfg)
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("quic dial %s: %w", target, err)
	}

	return conn, nil
}

func (d *QuicDialer) getPreferredIP() string {
	if d.ipProvider == nil {
		return ""
	}
	return d.ipProvider.GetIP()
}
