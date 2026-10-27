package config

import (
	"reflect"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ServerPort:        "8080",
		StoreDriver:       "redis",
		IPLimit:           10,
		DefaultTokenLimit: 100,
		TokenLimits:       map[string]int{},
		BlockDuration:     5 * time.Minute,
		FailOpen:          true,
		Redis:             Redis{Addr: "localhost:6379"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"SERVER_PORT":              "9000",
		"STORE_DRIVER":             "Memory",
		"RATE_LIMIT_IP":            "3",
		"RATE_LIMIT_TOKEN_DEFAULT": "30",
		"TOKEN_LIMITS":             "abc:100, vip:500",
		"BLOCK_DURATION":           "30s",
		"FAIL_OPEN":                "false",
		"TRUST_PROXY_HEADERS":      "true",
		"REDIS_ADDR":               "redis:6379",
		"REDIS_PASSWORD":           "secret",
		"REDIS_DB":                 "2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ServerPort:        "9000",
		StoreDriver:       "memory",
		IPLimit:           3,
		DefaultTokenLimit: 30,
		TokenLimits:       map[string]int{"abc": 100, "vip": 500},
		BlockDuration:     30 * time.Second,
		FailOpen:          false,
		TrustProxyHeaders: true,
		Redis:             Redis{Addr: "redis:6379", Password: "secret", DB: 2},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadFromRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"ip limit not a number":  {"RATE_LIMIT_IP": "abc"},
		"ip limit zero":          {"RATE_LIMIT_IP": "0"},
		"token default negative": {"RATE_LIMIT_TOKEN_DEFAULT": "-1"},
		"block duration":         {"BLOCK_DURATION": "five minutes"},
		"block duration zero":    {"BLOCK_DURATION": "0s"},
		"bool":                   {"FAIL_OPEN": "maybe"},
		"redis db":               {"REDIS_DB": "-1"},
		"token limits":           {"TOKEN_LIMITS": "abc"},
	}
	for name, vars := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFrom(env(vars)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseTokenLimits(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]int
		wantErr bool
	}{
		{"empty", "", map[string]int{}, false},
		{"single", "abc:100", map[string]int{"abc": 100}, false},
		{"several with spaces and trailing comma", " a:1 , b:2 ,", map[string]int{"a": 1, "b": 2}, false},
		{"token containing colon", "a:b:7", map[string]int{"a:b": 7}, false},
		{"missing limit", "abc", nil, true},
		{"empty token", ":5", nil, true},
		{"non numeric", "abc:x", nil, true},
		{"zero", "abc:0", nil, true},
		{"duplicate", "abc:1,abc:2", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTokenLimits(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
