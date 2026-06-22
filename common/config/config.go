package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/sajadweb/nika"
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
    
    // تزریق اتوماتیک به کانتینر فریمورک اصلی
    // (شما باید متد SetContainer یا مشابه آن را در App عمومی کنید، 
    // یا از یک متد UseDI استفاده کنید. ساده‌ترین راه اضافه کردن متد زیر در app.go است)
    app.RegisterSingleton(cfg) 
    
    return cfg
}

// Get returns the environment variable value for key, or the provided default.
func (c *Config) Get(key string, defaultValue ...string) string {
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
	s := c.Get(key)
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
	s := c.Get(key)
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