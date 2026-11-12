package tello

import (
	"errors"
	"os"
	"time"
)

const (
	DefaultURL = "wss://api.telloai.io/sdk"
	EnvAPIKey  = "TELLO_API_KEY"
	EnvURL     = "TELLO_URL"
)

type Config struct {
	APIKey       string
	URL          string
	OpenTimeout  time.Duration
	CloseTimeout time.Duration
}

type Option func(*Config)

func WithURL(url string) Option {
	return func(config *Config) {
		config.URL = url
	}
}

func WithOpenTimeout(timeout time.Duration) Option {
	return func(config *Config) {
		config.OpenTimeout = timeout
	}
}

func WithCloseTimeout(timeout time.Duration) Option {
	return func(config *Config) {
		config.CloseTimeout = timeout
	}
}

func resolveConfig(apiKey string, options ...Option) (Config, error) {
	apiKey, err := resolveAPIKey(apiKey)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		APIKey:       apiKey,
		URL:          getenv(EnvURL, DefaultURL),
		OpenTimeout:  10 * time.Second,
		CloseTimeout: 5 * time.Second,
	}
	for _, option := range options {
		option(&config)
	}
	return config, nil
}

// resolveAPIKey returns apiKey, or TELLO_API_KEY when apiKey is empty. The
// WebSocket client and the REST API share this rule and its error.
func resolveAPIKey(apiKey string) (string, error) {
	if apiKey == "" {
		apiKey = os.Getenv(EnvAPIKey)
	}
	if apiKey == "" {
		return "", errors.New("apiKey is required")
	}
	return apiKey, nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
