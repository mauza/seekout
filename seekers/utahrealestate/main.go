package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/go-redis/redis/v8"
	nats "github.com/nats-io/nats.go"

	"github.com/mauza/seekout/lib"
	"github.com/mauza/seekout/seekers/utahrealestate/internal"
)

var (
	BaseURL = "https://v12services.utahrealestate.com/property-search/property-search?orderby[0]=default"
)

func pollProperties(ctx context.Context, rc *redis.Client, nc *nats.Conn, setName string, topicName string) error {
	response, err := http.Get(BaseURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	var data map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		return err
	}
	dataField, ok := data["data"]
	if !ok {
		return fmt.Errorf("response didn't have data")
	}

	var properties []lib.Property
	if err := json.Unmarshal(dataField, &properties); err != nil {
		fmt.Print(err.Error())
		return fmt.Errorf("failed to unmarshal data")
	}

	count := 0

	for _, property := range properties {
		exists, err := rc.SIsMember(ctx, setName, property.ListNo).Result()
		if err != nil {
			log.Printf("failed to check redis set membership: %v", err)
		}
		if exists {
			log.Printf("already published listing %d", property.ListNo)
			break
		}
		propertyBytes, err := json.Marshal(property)
		if err != nil {
			log.Printf("Failed to marshal property: %v", err)
			continue
		}
		if err := nc.Publish(topicName, propertyBytes); err != nil {
			log.Fatalf("Error publishing message: %v", err)
		}
		log.Printf("Published property: %d", property.ListNo)
		rc.SAdd(ctx, setName, property.ListNo)
		count++
		break
	}

	log.Printf("Published %d properties", count)
	return nil
}

func main() {
	ctx := context.Background()
	config, err := internal.LoadConfig()
	if err != nil {
		fmt.Print(err)
	}
	nc, err := nats.Connect(config.NatsAddr)
	if err != nil {
		fmt.Print(err)
	}
	rc := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})
	err = pollProperties(ctx, rc, nc, config.RedisSetName, config.NatsTopic)
	if err != nil {
		fmt.Print(err)
	}
}
