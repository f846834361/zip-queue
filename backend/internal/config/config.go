package config

import (
	"fmt"
	"os"
	"strconv"
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
	// MaxExtractTotalBytes 单任务解压总字节上限（zip bomb 防护），0 表示不限制。
	MaxExtractTotalBytes int64 `yaml:"max_extract_total_bytes"`
	// MaxExtractRatio 允许的最大压缩率（解压后大小 / 原压缩包大小），0 表示不限制。
	MaxExtractRatio int64 `yaml:"max_extract_ratio"`
}

type BrowseConfig struct {
	DefaultPath string `yaml:"default_path"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

// Load 从给定路径读取并解析 YAML 配置，填充默认值，
// 再用环境变量覆盖（容器部署无需重建镜像即可调整配置）。
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
	if err := c.applyEnvOverrides(); err != nil {
		return nil, err
	}
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
	if c.Worker.MaxExtractRatio == 0 {
		c.Worker.MaxExtractRatio = 100
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
}

// applyEnvOverrides 用环境变量覆盖已加载的配置。变量未设置时保持 YAML 值；
// 已设置但格式非法时返回错误（让部署期问题尽早暴露）。
func (c *Config) applyEnvOverrides() error {
	if v := os.Getenv("SERVER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SERVER_PORT %q: %w", v, err)
		}
		c.Server.Port = n
	}
	if v := os.Getenv("SERVER_READ_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid SERVER_READ_TIMEOUT %q: %w", v, err)
		}
		c.Server.ReadTimeout = d
	}
	if v := os.Getenv("SERVER_WRITE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid SERVER_WRITE_TIMEOUT %q: %w", v, err)
		}
		c.Server.WriteTimeout = d
	}
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		c.DB.Path = v
	}
	if v := os.Getenv("WORKER_MAX_CONCURRENT_TASKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid WORKER_MAX_CONCURRENT_TASKS %q: %w", v, err)
		}
		c.Worker.MaxConcurrentTasks = n
	}
	if v := os.Getenv("WORKER_MAX_EXTRACT_TOTAL_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid WORKER_MAX_EXTRACT_TOTAL_BYTES %q: %w", v, err)
		}
		c.Worker.MaxExtractTotalBytes = n
	}
	if v := os.Getenv("WORKER_MAX_EXTRACT_RATIO"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid WORKER_MAX_EXTRACT_RATIO %q: %w", v, err)
		}
		c.Worker.MaxExtractRatio = n
	}
	if v := os.Getenv("BROWSE_DEFAULT_PATH"); v != "" {
		c.Browse.DefaultPath = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	return nil
}
