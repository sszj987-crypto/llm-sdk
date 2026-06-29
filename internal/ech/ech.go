package ech

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

const (
	dohTimeout       = 10 * time.Second
	handshakeTimeout = 15 * time.Second
)

var dohServers = []string{
	"https://cloudflare-dns.com/dns-query",
	"https://dns.google/dns-query",
	"https://1.1.1.1/dns-query",
}

// IPProvider is an interface for providing preferred CF IPs at runtime.
type IPProvider interface {
	GetIP() string
}

// retrieveECHConfig queries a domain's HTTPS DNS record via DoH.
func retrieveECHConfig(ctx context.Context, domain string) ([]byte, error) {
	for _, dohURL := range dohServers {
		config, err := fetchHTTPSRecord(ctx, dohURL, domain)
		if err != nil {
			log.Printf("[ech] doh_query_failed server=%s domain=%s err=%v", dohURL, domain, err)
			continue
		}
		if config != nil {
			log.Printf("[ech] echconfig_retrieved domain=%s doh_server=%s config_len=%d", domain, dohURL, len(config))
			return config, nil
		}
	}
	return nil, fmt.Errorf("ech: no ECH config found for %s across all DoH servers", domain)
}

func fetchHTTPSRecord(ctx context.Context, dohURL, domain string) ([]byte, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(domain), dns.TypeHTTPS)
	msg.SetEdns0(4096, false)

	packed, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack dns query: %w", err)
	}

	client := &http.Client{Timeout: dohTimeout}
	dnsReq, err := http.NewRequestWithContext(ctx, http.MethodPost, dohURL, bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	dnsReq.Header.Set("Content-Type", "application/dns-message")
	dnsReq.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(dnsReq)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("doh status %d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read doh body: %w", err)
	}

	reply := new(dns.Msg)
	if err := reply.Unpack(body); err != nil {
		return nil, fmt.Errorf("unpack dns response: %w", err)
	}

	for _, rr := range reply.Answer {
		https, ok := rr.(*dns.HTTPS)
		if !ok || https == nil {
			continue
		}
		for _, svc := range https.Value {
			if svc.Key() == dns.SVCB_ECHCONFIG {
				if ech, ok := svc.(*dns.SVCBECHConfig); ok {
					return ech.ECH, nil
				}
			}
		}
	}
	return nil, nil
}

// Dialer is a TLS dialer with ECH and preferred-IP support.
type Dialer struct {
	Port      string
	EnableECH bool

	// ipProvider is called each time a connection is needed to get a fresh preferred IP.
	// When set, the IP it returns is used as the dial target (SNI remains the domain).
	ipProvider IPProvider

	echConfigCache map[string][]byte
}

// NewDialer creates a new ECH-aware dialer.
func NewDialer() *Dialer {
	return &Dialer{
		Port:           "443",
		EnableECH:      false,
		echConfigCache: make(map[string][]byte),
	}
}

// SetIPProvider sets a runtime IP source for preferred CF edge IPs.
func (d *Dialer) SetIPProvider(p IPProvider) {
	d.ipProvider = p
}

// SetEnableECH enables or disables ECH.
func (d *Dialer) SetEnableECH(enabled bool) {
	d.EnableECH = enabled
}

// DialTLSContext establishes a TLS connection to domain.
// If ipProvider is set, dials the IP it provides while keeping SNI as domain.
// If EnableECH is true, fetches and uses the ECHConfig for the domain.
func (d *Dialer) DialTLSContext(ctx context.Context, network, domain string) (net.Conn, error) {
	var ipSource string
	addr := domain + ":" + d.Port
	if p := d.getPreferredIP(); p != "" {
		addr = p + ":" + d.Port
		ipSource = "ipdb"
	} else {
		ipSource = "dns"
	}
	log.Printf("[ech] dial domain=%s addr=%s source=%s", domain, addr, ipSource)

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: domain,
	}

	if d.EnableECH {
		echConfig, err := d.getECHConfig(ctx, domain)
		if err != nil {
			log.Printf("[ech] ech_config_failed domain=%s err=%v — falling back to no ECH", domain, err)
		} else if len(echConfig) > 0 {
			tlsConfig.EncryptedClientHelloConfigList = echConfig
			log.Printf("[ech] ech_enabled domain=%s ech_config_len=%d", domain, len(echConfig))
		}
	}

	dialer := &net.Dialer{Timeout: handshakeTimeout}
	rawConn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	tlsConn := tls.Client(rawConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		if d.EnableECH {
			if reject, ok := err.(*tls.ECHRejectionError); ok {
				log.Printf("[ech] ech_rejected domain=%s — retrying with server config", domain)
				return d.retryECH(ctx, network, addr, domain, reject.RetryConfigList)
			}
		}
		return nil, fmt.Errorf("tls handshake to %s: %w", addr, err)
	}

	return tlsConn, nil
}

func (d *Dialer) getPreferredIP() string {
	if d.ipProvider == nil {
		return ""
	}
	return d.ipProvider.GetIP()
}

func (d *Dialer) retryECH(ctx context.Context, network, addr, domain string, retryConfig []byte) (net.Conn, error) {
	tlsConfig := &tls.Config{
		MinVersion:                     tls.VersionTLS13,
		ServerName:                     domain,
		EncryptedClientHelloConfigList: retryConfig,
	}

	dialer := &net.Dialer{Timeout: handshakeTimeout}
	rawConn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial retry %s: %w", addr, err)
	}

	tlsConn := tls.Client(rawConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("tls retry handshake to %s: %w", addr, err)
	}

	return tlsConn, nil
}

func (d *Dialer) getECHConfig(ctx context.Context, domain string) ([]byte, error) {
	if config, ok := d.echConfigCache[domain]; ok {
		return config, nil
	}

	ctx, cancel := context.WithTimeout(ctx, dohTimeout)
	defer cancel()

	config, err := retrieveECHConfig(ctx, domain)
	if err != nil {
		return nil, err
	}

	d.echConfigCache[domain] = config
	return config, nil
}
