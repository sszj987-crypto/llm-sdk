package provider

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"llm-sdk/internal/config"
	"llm-sdk/internal/dialer"
	"llm-sdk/internal/ipdb"
)

func TestCompareScenarios(t *testing.T) {
	configPath := os.Getenv("LLM_CONFIG_PATH")
	if configPath == "" {
		configPath = "../../config.json"
	}
	store := config.NewStore(configPath)
	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.APIKey == "" || cfg.WorkerURL == "" {
		t.Skip("need apiKey and workerUrl in config.json to run comparison test")
	}

	providerCfg := Config{
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		WorkerURL: cfg.WorkerURL,
	}

	ipdbCli := ipdb.NewClient()
	defer ipdbCli.Stop()

	tcpClient, tcpDialer := buildTCPClient(ipdbCli)
	quicClient, quicDialer := buildQUICClient(ipdbCli, &quic.Config{
		HandshakeIdleTimeout: 15 * time.Second,
	})
	quicTunedClient, quicTunedDialer := buildQUICClient(ipdbCli, &quic.Config{
		HandshakeIdleTimeout:       10 * time.Second,
		Allow0RTT:                  true,
		InitialStreamReceiveWindow: 10 * 1024 * 1024,
		DisablePathMTUDiscovery:    true,
		InitialPacketSize:          1400,
	})

	messages := []Message{{Role: "user", Content: "hi"}}

	type scenario struct {
		name       string
		client     *http.Client
		tcpDialer  *dialer.Dialer
		quicDialer *dialer.QuicDialer
	}
	scenarios := []scenario{
		{"worker_tcp", tcpClient, tcpDialer, nil},
		{"worker_quic", quicClient, nil, quicDialer},
		{"worker_quic_tuned", quicTunedClient, nil, quicTunedDialer},
	}

	type result struct {
		name       string
		total      time.Duration
		summary    map[string]time.Duration
		deltaCount int
		err        error
		// dialer stage timing
		dialStage time.Duration // TCP: DNS+connect, QUIC: DNS resolve
		tlsStage  time.Duration // TCP: TLS handshake (from dialer), QUIC: -
		quicHS    time.Duration // QUIC: handshake, TCP: -
	}
	results := make([]result, len(scenarios))

	for i, sc := range scenarios {
		adapter := NewGeminiAdapter(sc.client)
		fmt.Printf("[test:%s] start\n", sc.name)
		totalStart := time.Now()

		var deltaCount int
		err := adapter.Chat(context.Background(), providerCfg, messages, func(delta string) error {
			deltaCount++
			return nil
		})
		totalElapsed := time.Since(totalStart)
		fmt.Printf("[test:%s] done total=%s err=%v\n", sc.name, totalElapsed.Round(time.Millisecond), err)

		var summary map[string]time.Duration
		if adapter.LastTrace != nil {
			summary = adapter.LastTrace.Summary()
		}

		r := result{
			name:       sc.name,
			total:      totalElapsed,
			summary:    summary,
			deltaCount: deltaCount,
			err:        err,
		}
		if sc.tcpDialer != nil {
			r.dialStage = sc.tcpDialer.LastDial
			r.tlsStage = sc.tcpDialer.LastTLS
		}
		if sc.quicDialer != nil {
			r.dialStage = sc.quicDialer.LastResolve
			r.quicHS = sc.quicDialer.LastHandshake
		}
		results[i] = r
	}

	fmt.Println()
	fmt.Println("=== 场景对比 ===")
	fmt.Printf("%-25s %-15s %-15s %-15s\n", "指标", "worker_tcp", "worker_quic", "worker_quic_tuned")
	fmt.Println("---------------------------------------------------------------------")

	printRow := func(label string, get func(result) string) {
		fmt.Printf("%-25s %-15s %-15s %-15s\n",
			label,
			get(results[0]),
			get(results[1]),
			get(results[2]),
		)
	}

	dur := func(d time.Duration) string { return d.Round(time.Millisecond).String() }

	val := func(r result, key string) string {
		d, ok := r.summary[key]
		if !ok {
			return "-"
		}
		return dur(d)
	}

	printRow("--- dialer stages ---", func(r result) string { return "" })
	printRow("  dns+connect/resolve", func(r result) string {
		if r.dialStage == 0 {
			return "-"
		}
		return dur(r.dialStage)
	})
	printRow("  tls (dialer)", func(r result) string {
		if r.tlsStage == 0 {
			return "-"
		}
		return dur(r.tlsStage)
	})
	printRow("  quic_handshake", func(r result) string {
		if r.quicHS == 0 {
			return "-"
		}
		return dur(r.quicHS)
	})
	printRow("--- httptrace ---", func(r result) string { return "" })
	printRow("  get_conn", func(r result) string { return val(r, "get_conn") })
	printRow("  got_conn (conn ready)", func(r result) string { return val(r, "got_conn") })
	printRow("  wrote_request", func(r result) string { return val(r, "wrote_request") })
	printRow("  first_byte (TTFB)", func(r result) string { return val(r, "first_byte") })
	printRow("--- derived ---", func(r result) string { return "" })
	printRow("  server_wait", func(r result) string {
		wrote, ok1 := r.summary["wrote_request"]
		firstByte, ok2 := r.summary["first_byte"]
		if !ok1 || !ok2 {
			return "-"
		}
		return dur(firstByte - wrote)
	})
	printRow("  total", func(r result) string { return dur(r.total) })

	for _, r := range results {
		if r.err != nil {
			fmt.Printf("\n[%s] error: %v\n", r.name, r.err)
		}
	}
}

func buildTCPClient(ipdbCli *ipdb.Client) (*http.Client, *dialer.Dialer) {
	echDialer := dialer.New()
	if ipdbCli != nil && ipdbCli.Count() > 0 {
		echDialer.SetIPProvider(ipdbCli)
	}
	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if port != "" {
				echDialer.Port = port
			}
			return echDialer.DialTLSContext(ctx, network, host)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
		},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport}, echDialer
}

func buildQUICClient(ipdbCli *ipdb.Client, quicCfg *quic.Config) (*http.Client, *dialer.QuicDialer) {
	quicDialer := dialer.NewQuicDialer()
	if ipdbCli != nil && ipdbCli.Count() > 0 {
		quicDialer.SetIPProvider(ipdbCli)
	}
	qt := &http3.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
		},
		QUICConfig: quicCfg,
		Dial:       quicDialer.Dial,
	}
	return &http.Client{Transport: qt}, quicDialer
}
