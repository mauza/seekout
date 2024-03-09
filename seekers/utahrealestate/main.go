package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"internal"

	"cloud.google.com/go/datastore"
)

var (
	PublishTopic  = os.Getenv("PUBLISH_TOPIC")
	ProjectID     = os.Getenv("PROJECT_ID")
	BaseURL       = "https://v12services.utahrealestate.com/property-search/property-search?orderby[0]=default"
	DatastoreKind = "LastProperty"
)

func pollProperties(ctx context.Context) error {
	client, err := datastore.NewClient(ctx, ProjectID)
	if err != nil {
		return err
	}
	defer client.Close()

	response, err := http.Get(BaseURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		return err
	}

	count := 0

	for _, property := range data["data"].([]internal.Property) {
		propertyBytes, err := json.Marshal(property)
		if err != nil {
			log.Printf("Failed to marshal property: %v", err)
			continue
		}
		// Publish to Pub/Sub
		// Replace with your Pub/Sub publishing logic
		log.Printf("Published property: %s", propertyBytes)
		count++
	}

	log.Printf("Published %d properties", count)
	return nil
}

func main() {
	ctx := context.Background()
	err := pollProperties(ctx)
	if err != nil {
		fmt.Print(err.Error())
	}
}
