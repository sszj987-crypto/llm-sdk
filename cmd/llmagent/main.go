package main

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"llm-sdk/internal/config"
	"llm-sdk/internal/logging"
	"llm-sdk/internal/paths"
	"llm-sdk/internal/server"
)

func main() {
	projectRoot := mustProjectRoot()
	addr := envOrDefault("LLM_AGENT_ADDR", "127.0.0.1:8787")
	configPath := paths.DefaultConfigPath(projectRoot)
	logDir := paths.DefaultLogDir(projectRoot)

	logFile, err := logging.Setup(logDir)
	if err != nil {
		log.Fatalf("init logging: %v", err)
	}
	defer func() {
		_ = logFile.Close()
	}()
	log.Printf("paths ready config_path=%s log_dir=%s", configPath, logDir)

	migrateLegacyConfig(filepath.Join(projectRoot, "cmd", "llmagent", "config.json"), configPath)
	migrateLegacyConfig(filepath.Join(projectRoot, "config.json"), configPath)

	store := config.NewStore(configPath)
	appServer, err := server.New(store)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	mux := http.NewServeMux()
	appServer.Register(mux)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	openBrowser("http://" + addr)

	select {}
}

func mustProjectRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}

	current := filepath.Dir(thisFile)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

func migrateLegacyConfig(legacyPath, targetPath string) {
	if _, err := os.Stat(targetPath); err == nil {
		return
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return
	}
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		log.Printf("migrate legacy config failed legacy=%s target=%s err=%v", legacyPath, targetPath, err)
		return
	}
	log.Printf("migrated legacy config legacy=%s target=%s", legacyPath, targetPath)
}

func envOrDefault(name, fallback string) string {
	value := os.Getenv(name)
	if value != "" {
		return value
	}
	return fallback
}

func openBrowser(targetURL string) {
	if os.Getenv("LLM_AGENT_NO_BROWSER") == "1" {
		return
	}
	if runtime.GOOS != "darwin" {
		log.Printf("open browser manually: %s", targetURL)
		return
	}
	if err := exec.Command("open", targetURL).Run(); err != nil {
		log.Printf("open browser manually: %s", targetURL)
	}
}
