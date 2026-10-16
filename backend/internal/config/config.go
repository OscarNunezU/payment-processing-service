package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	CardEncryptionKey []byte
	MaxAmounts        map[string]int64 // currency → maximum amount, in minor units
	AcquirerURL       string
	AcquirerTimeout   time.Duration
	WorkerConcurrency int
}

// Load fails if DATABASE_URL is missing, if CARD_ENCRYPTION_KEY is not 32 bytes
// in base64, or if a setting with a default holds an invalid value.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AcquirerURL: getEnv("ACQUIRER_URL", "http://localhost:8081"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	key, err := base64.StdEncoding.DecodeString(os.Getenv("CARD_ENCRYPTION_KEY"))
	if err != nil || len(key) != 32 {
		return Config{}, errors.New("CARD_ENCRYPTION_KEY must be 32 bytes encoded in base64")
	}
	cfg.CardEncryptionKey = key

	clp, err := getPositiveInt("MAX_AMOUNT_CLP", 5_000_000)
	if err != nil {
		return Config{}, err
	}
	usd, err := getPositiveInt("MAX_AMOUNT_USD", 500_000)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxAmounts = map[string]int64{"CLP": clp, "USD": usd}

	if cfg.AcquirerTimeout, err = getPositiveDuration("ACQUIRER_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	workers, err := getPositiveInt("WORKER_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	cfg.WorkerConcurrency = int(workers)

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getPositiveInt(key string, fallback int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, v)
	}
	return n, nil
}

func getPositiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 5s, got %q", key, v)
	}
	return d, nil
}
