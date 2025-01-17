package model

import (
	"github.com/spf13/viper"
)

type Config struct {
	IsOutsideCluster bool            `json:"isOutsideCluster"`
	Kubeconfig       string          `json:"kubeconfig"`
	IngressBaseUrl   string          `json:"ingressBaseUrl"`
	IngressClass     string          `json:"ingressClass"`
	DBConfig         *DBConfig       `json:"database"`
	ResourceConfig   *ResourceConfig `json:"resourceLimit,omitempty"`
	RedisConfig      *RedisConfig    `json:"redis"`
}

type DBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
}

type RedisConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type ResourceConfig struct {
	Factor  int64  `json:"factor"`
	Cpu     string `json:"cpu"`
	Memory  string `json:"memory"`
	Storage string `json:"storage"`
}

func NewConfig(config *viper.Viper) (*Config, error) {
	confObj := Config{}
	if err := config.Unmarshal(&confObj); err != nil {
		return nil, err
	}

	dbconfig := DBConfig{}
	if err := config.UnmarshalKey("database", &dbconfig); err != nil {
		return nil, err
	}
	confObj.DBConfig = &dbconfig

	redisconfig := RedisConfig{}
	if err := config.UnmarshalKey("redis", &redisconfig); err != nil {
		return nil, err
	}
	confObj.RedisConfig = &redisconfig

	if config.InConfig("resourcelimit") {
		resoureconfig := ResourceConfig{}
		if err := config.UnmarshalKey("resourceLimit", &resoureconfig); err != nil {
			return nil, err
		}
		confObj.ResourceConfig = &resoureconfig
	}

	return &confObj, nil
}
