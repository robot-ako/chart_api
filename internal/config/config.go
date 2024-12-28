package config

import (
	"github.com/spf13/viper"
)

type Config struct {
	GRPC GRPCConfig `mapstructure:"grpc"`
	DB   DBConfig   `mapstructure:"db"`
}

type GRPCConfig struct {
	Server GRPCServerConfig `mapstructure:"server"`
	Client GRPCClientConfig `mapstructure:"client"`
}

type GRPCServerConfig struct {
	Host string `mapstructure:"host"`
	Port string `mapstructure:"port"`
}

type GRPCClientConfig struct {
	Host string `mapstructure:"host"`
	Port string `mapstructure:"port"`
}

type DBConfig struct {
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Host     string `mapstructure:"host"`
	Port     string `mapstructure:"port"`
	DBName   string `mapstructure:"db_name"`
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
