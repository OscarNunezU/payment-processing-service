package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	CardEncryptionKey []byte
	MaxAmounts        map[string]int64 // currency → maximum amount, in minor units
}

// Load fails if DATABASE_URL or CARD_ENCRYPTION_KEY is missing or invalid.
// HTTP_ADDR and the maximum amounts have defaults.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
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
