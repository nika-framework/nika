package config

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/nika-framework/nika"
)

// Use LoadConfig to create one.
type Config struct{}


// LoadConfig loads the given .env file (or the default .env when path == "")
// and returns a Config instance.
func Setup(app *nika.App, envPath string) *Config {
    if envPath == "" {
        _ = godotenv.Load()
    } else {
        _ = godotenv.Load(envPath)
    }
    
    cfg := &Config{}
    if(app != nil) {
   		 app.RegisterSingleton(cfg) 
	}
    return cfg
}

// GetString returns the environment variable value for key, or the provided default.
func (c *Config) GetString(key string, defaultValue ...string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	if len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return ""
}

// GetInt returns the environment variable parsed as int, or the provided default.
func (c *Config) GetInt(key string, defaultValue ...int) int {
	s := c.GetString(key)
	if s == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	return i
}

// GetBool returns the environment variable parsed as bool, or the provided default.
func (c *Config) GetBool(key string, defaultValue ...bool) bool {
	s := c.GetString(key)
	if s == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return false
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return false
	}
	return b
}
// Get tries to parse the environment variable as JSON and unmarshal it into type T.
// If the variable is empty or parsing fails, defaultValue is returned.
func Get[T any](c *Config,key string, defaultValue T) T {
	s := c.GetString(key)
	if s == "" {
		return defaultValue
	}
	var result T
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return defaultValue
	}
	return result
}