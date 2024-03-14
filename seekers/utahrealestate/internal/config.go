package internal

import (
	"os"
	"strconv"
)

type Config struct {
	RedisAddr     string
	RedisDB       int
	RedisPassword string
	RedisSetName  string
	NatsAddr      string
	NatsTopic     string
}

var defaultConfig = Config{
	RedisAddr:    "localhost:6379",
	RedisDB:      0,
	RedisSetName: "processed_properties_7",
	NatsAddr:     "nats://localhost:4222",
	NatsTopic:    "properties",
}

func LoadConfig() (*Config, error) {
	config := defaultConfig
	redisAddr, ok := os.LookupEnv("REDIS_ADDR")
	if ok {
		config.RedisAddr = redisAddr
	}
	redisDB, ok := os.LookupEnv("REDIS_DB")
	if ok {
		redisDB, err := strconv.Atoi(redisDB)
		if err != nil {
			return nil, err
		}
		config.RedisDB = redisDB
	}
	redisPassword, ok := os.LookupEnv("REDIS_PASSWORD")
	if ok {
		config.RedisPassword = redisPassword
	}
	natsAddr, ok := os.LookupEnv("NATS_ADDR")
	if ok {
		config.NatsAddr = natsAddr
	}
	natsTopic, ok := os.LookupEnv("NATS_TOPIC")
	if ok {
		config.NatsTopic = natsTopic
	}

	return &config, nil
}
