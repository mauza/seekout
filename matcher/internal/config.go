package internal

import (
	"os"
	"strconv"
)

type Config struct {
	ProjectID     string
	RedisAddr     string
	RedisDB       int
	RedisPassword string
	NatsAddr      string
	NatsSubTopic  string
	NatsPubTopic  string
}

var defaultConfig = Config{
	ProjectID:    "salame-298421",
	RedisAddr:    "localhost:6379",
	RedisDB:      1,
	NatsAddr:     "nats://localhost:4222",
	NatsSubTopic: "properties",
	NatsPubTopic: "matched_properties",
}

func LoadConfig() (*Config, error) {
	config := defaultConfig
	projectID, ok := os.LookupEnv("PROJECT_ID")
	if ok {
		config.ProjectID = projectID
	}
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
	natsSubTopic, ok := os.LookupEnv("NATS_SUB_TOPIC")
	if ok {
		config.NatsSubTopic = natsSubTopic
	}
	natsPubTopic, ok := os.LookupEnv("NATS_PUB_TOPIC")
	if ok {
		config.NatsPubTopic = natsPubTopic
	}

	return &config, nil
}
