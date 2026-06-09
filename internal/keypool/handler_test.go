package keypool

import "testing"

func TestModelsURLFromBase(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "standard v1 chat completions",
			baseURL: "https://api.example.com/v1/chat/completions",
			want:    "https://api.example.com/v1/models",
		},
		{
			name:    "preserves prefix before v1",
			baseURL: "https://proxy.example.com/openai/v1/chat/completions",
			want:    "https://proxy.example.com/openai/v1/models",
		},
		{
			name:    "uses full configured path without v1",
			baseURL: "https://proxy.example.com/openai/chat/completions",
			want:    "https://proxy.example.com/openai/chat/completions",
		},
		{
			name:    "trims trailing slash and query",
			baseURL: "https://proxy.example.com/openai/v1/chat/completions/?api-version=2024-02-01",
			want:    "https://proxy.example.com/openai/v1/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelsURLFromBase(tt.baseURL); got != tt.want {
				t.Fatalf("modelsURLFromBase(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestChatCompletionsURLFromBase(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "standard v1 root",
			baseURL: "https://api.example.com/v1",
			want:    "https://api.example.com/v1/chat/completions",
		},
		{
			name:    "standard v1 chat completions",
			baseURL: "https://api.example.com/v1/chat/completions",
			want:    "https://api.example.com/v1/chat/completions",
		},
		{
			name:    "preserves prefix before v1",
			baseURL: "https://proxy.example.com/openai/v1/chat/completions",
			want:    "https://proxy.example.com/openai/v1/chat/completions",
		},
		{
			name:    "uses full configured path without v1",
			baseURL: "https://proxy.example.com/openai",
			want:    "https://proxy.example.com/openai",
		},
		{
			name:    "trims trailing slash and query",
			baseURL: "https://proxy.example.com/openai/v1/?api-version=2024-02-01",
			want:    "https://proxy.example.com/openai/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatCompletionsURLFromBase(tt.baseURL); got != tt.want {
				t.Fatalf("chatCompletionsURLFromBase(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}
