package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 zip-queue 运行配置根。
type Config struct {
	Server ServerConfig `yaml:"server"`
	DB     DBConfig     `yaml:"database"`
	Worker WorkerConfig `yaml:"worker"`
	Browse BrowseConfig `yaml:"browse"`
	Log    LogConfig    `yaml:"log"`
}

type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type DBConfig struct {
	Path string `yaml:"path"`
}

type WorkerConfig struct {
	MaxConcurrentTasks int `yaml:"max_concurrent_tasks"`
}

type BrowseConfig struct {
	DefaultPath string `yaml:"default_path"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

// Load 从给定路径读取并解析 YAML 配置，填充默认值。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 8787
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 60 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 60 * time.Second
	}
	if c.DB.Path == "" {
		c.DB.Path = "./data/zip-queue.db"
	}
	if c.Worker.MaxConcurrentTasks <= 0 {
		c.Worker.MaxConcurrentTasks = 1
	}
	if c.Browse.DefaultPath == "" {
		c.Browse.DefaultPath = "/"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
}
