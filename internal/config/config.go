package config

import (
	"strings"

	"github.com/spf13/viper"
)

// ServerConfig tracks the gateway port and downstream backends.
type ServerConfig struct {
	Port     string   `mapstructure:"port"`
	Backends []string `mapstructure:"backends"`
}

// RateLimitConfig contains token bucket rate limiting parameters.
type RateLimitConfig struct {
	Rate     float64 `mapstructure:"rate"`
	Capacity float64 `mapstructure:"capacity"`
}

// EncryptionConfig holds the cipher secret key.
type EncryptionConfig struct {
	Key string `mapstructure:"key"`
}

// ClusterConfig configures the gossip membership settings.
type ClusterConfig struct {
	NodeName    string `mapstructure:"node_name"`
	BindAddr    string `mapstructure:"bind_addr"`
	BindPort    int    `mapstructure:"bind_port"`
	JoinAddress string `mapstructure:"join_address"`
}

// Config represents the unified AeroProxy configuration schema.
type Config struct {
	LogLevel   string           `mapstructure:"log_level"`
	Server     ServerConfig     `mapstructure:"server"`
	RateLimit  RateLimitConfig  `mapstructure:"ratelimit"`
	Encryption EncryptionConfig `mapstructure:"encryption"`
	Cluster    ClusterConfig    `mapstructure:"cluster"`
}

// LoadConfig reads the config file from the specified path using Viper,
// falling back to defaults if the file is absent.
func LoadConfig(path string) (*Config, error) {
	v := viper.New()

	// Default configurations
	v.SetDefault("log_level", "info")
	v.SetDefault("server.port", ":8080")
	v.SetDefault("server.backends", []string{
		"http://localhost:8081",
		"http://localhost:8082",
		"http://localhost:8083",
	})
	v.SetDefault("ratelimit.rate", 5.0)
	v.SetDefault("ratelimit.capacity", 10.0)
	v.SetDefault("encryption.key", "0123456789abcdef0123456789abcdef")

	v.SetDefault("cluster.node_name", "aeroproxy-node-1")
	v.SetDefault("cluster.bind_addr", "0.0.0.0")
	v.SetDefault("cluster.bind_port", 7946)
	v.SetDefault("cluster.join_address", "")

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
	}

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind custom env vars for Podman compatibility
	_ = v.BindEnv("log_level", "LOG_LEVEL")
	_ = v.BindEnv("cluster.node_name", "NODE_NAME")
	_ = v.BindEnv("cluster.bind_addr", "BIND_ADDR")
	_ = v.BindEnv("cluster.join_address", "CLUSTER_JOIN_ADDRESS")

	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
