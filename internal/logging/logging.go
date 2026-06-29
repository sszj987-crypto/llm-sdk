package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

type Sink struct {
	mu   sync.Mutex
	file *os.File
	out  io.Writer
}

var defaultSink *Sink

func Setup(logDir string) (*Sink, error) {
	if logDir == "" {
		return nil, fmt.Errorf("log dir is empty")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}

	filePath := fmt.Sprintf("%s/llm-agent.log", logDir)
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}

	sink := &Sink{
		file: file,
		out:  file,
	}
	defaultSink = sink
	log.SetOutput(sink)
	fmt.Fprintf(sink, "logging initialized log_dir=%s log_file=%s\n", logDir, filePath)
	return sink, nil
}

func Writer() io.Writer {
	if defaultSink != nil {
		return defaultSink
	}
	return os.Stderr
}

func (s *Sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}

func (s *Sink) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	return s.file.Close()
}
