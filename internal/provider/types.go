package provider

import "context"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	WorkerURL string
}

type StreamHandler func(delta string) error

type Adapter interface {
	Chat(ctx context.Context, cfg Config, messages []Message, onDelta StreamHandler) error
}
