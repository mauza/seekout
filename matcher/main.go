package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mauza/seekout/lib"
	"github.com/mauza/seekout/matcher/internal"
	"google.golang.org/api/iterator"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go"
	"github.com/go-redis/redis/v8"
	"github.com/nats-io/nats.go"
)

func main() {
	ctx := context.Background()
	config, err := internal.LoadConfig()
	if err != nil {
		log.Fatalf("error getting config: %v", err)
		return
	}
	// Initialize Firebase app and Firestore client
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: config.ProjectID})
	if err != nil {
		log.Fatalf("error initializing app: %v", err)
		return
	}
	firestore, err := app.Firestore(ctx)
	if err != nil {
		log.Fatalf("error getting database client: %v", err)
		return
	}
	rc := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})
	nc, err := nats.Connect(config.NatsAddr)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()
	m := &Matcher{
		firestore: firestore,
		rc:        rc,
		nc:        nc,
		pubTopic:  config.NatsPubTopic,
	}
	_, err = nc.Subscribe(config.NatsSubTopic, m.processMessage)
	if err != nil {
		log.Fatalf("Failed to subscribe to topic '%s': %v", config.NatsSubTopic, err)
	}
	select {}
}

type Matcher struct {
	firestore *firestore.Client
	rc        *redis.Client
	nc        *nats.Conn
	pubTopic  string
}

func (m *Matcher) processMessage(msg *nats.Msg) {
	ctx := context.Background()
	var property lib.Property
	err := json.Unmarshal(msg.Data, &property)
	if err != nil {
		log.Printf("failed to unmarshal seeker json, %v", err)
	}
	fmt.Println("processing property ", property.ListNo)
	users, err := m.firestore.Collection("users").DocumentRefs(ctx).GetAll()
	if err != nil {
		log.Printf("failed to get users: %v", err)
		return
	}
	fmt.Println("# of users ", len(users))

	for _, userRef := range users {
		userID := userRef.ID
		fmt.Println("processing for user ", userID)
		// var sc internal.SeekerConditions
		// scBytes, err := m.rc.Get(ctx, userID).Bytes()
		// if err == redis.Nil || scBytes == nil {
		sc, err := m.getSeekersConditions(ctx, userID)
		if err != nil {
			fmt.Println("error getting seeker conditions", err)
			continue
		}
		err = m.matchConditions(property, userID, sc)
		if err != nil {
			fmt.Println("error matching conditions", err, userID, property, sc)
			continue
		}
		// Set the value with the key and TTL
		data, err := json.Marshal(sc)
		if err != nil {
			fmt.Println("error marshalling conditions", err)
		}
		err = m.rc.Set(ctx, userID, data, 24*time.Hour).Err()
		if err != nil {
			fmt.Println("error setting value in Redis:", err)
			continue
		}
		fmt.Println("user seekers stored in Redis")
		// } else if err != nil {
		// 	fmt.Println("error getting value from Redis:", err)
		// 	continue
		// }
		// err = json.Unmarshal(scBytes, &sc)
		// if err != nil {
		// 	fmt.Println("error unmarshalling sc bytes")
		// }
		// err = m.matchConditions(property, userID, sc)
		// if err != nil {
		// 	fmt.Println("error matching cached conditions", err, userID, property, sc)
		// }
	}
}

func (m *Matcher) matchConditions(property lib.Property, userID string, seekerConditions internal.SeekerConditions) error {
	for seekerID, conditions := range seekerConditions {
		fmt.Println("checking conditions for seeker", seekerID)
		if propertyMatches(property, conditions) {
			data, err := json.Marshal(map[string]any{
				"user_id":   userID,
				"seeker_id": seekerID,
				"property":  property,
			})
			if err != nil {
				return err
			}
			m.nc.Publish(m.pubTopic, data)
			fmt.Println("published property ", property.ListNo)
		}
	}
	return nil
}

func (m *Matcher) getSeekersConditions(ctx context.Context, userID string) (internal.SeekerConditions, error) {
	conditions := make(internal.SeekerConditions)
	seekers := m.firestore.Collection(fmt.Sprintf("users/%s/seekers", userID)).DocumentRefs(ctx)
	for {
		seeker, err := seekers.Next()
		if err == iterator.Done {
			break
		} else if err != nil {
			return nil, err
		}
		snapshot, err := seeker.Get(ctx)
		if err != nil {
			fmt.Println("failed to get seeker doc snapshot")
			continue
		}
		if snapshot.Data()["type"] != "realestate" {
			continue
		}
		seekerID := seeker.ID
		seekerConditions, err := m.getConditionsForSeeker(ctx, userID, seekerID)
		if err != nil {
			log.Printf("Failed to get conditions for seeker %s: %v", seekerID, err)
			continue
		}
		conditions[seekerID] = seekerConditions
	}
	return conditions, nil
}

func (m *Matcher) getConditionsForSeeker(ctx context.Context, userID string, seekerID string) ([]internal.Condition, error) {
	var conditions []internal.Condition
	conditionRefs := m.firestore.Collection(fmt.Sprintf("users/%s/conditions", userID)).DocumentRefs(ctx)
	for {
		condition, err := conditionRefs.Next()
		if err == iterator.Done {
			break
		} else if err != nil {
			fmt.Println("failed to get next condition", err)
			break
		}

		snapshot, err := condition.Get(ctx)
		if err != nil {
			fmt.Println("failed to get condition doc snapshot")
		}

		data := snapshot.Data()
		if data["seeker_id"] == seekerID {
			fieldName, ok := data["fieldName"].(string)
			if !ok {
				fmt.Println("failed to convert fieldName to string")
				break
			}
			operator, ok := data["operator"].(string)
			if !ok {
				fmt.Println("failed to convert operator to string")
				break
			}
			value, ok := data["value"].(string)
			if !ok {
				fmt.Println("failed to convert value to string")
				break
			}
			var isList bool
			isListRaw, ok := data["isList"].(string)
			if !ok {
				fmt.Println("failed to convert value to string")
				break
			}
			if strings.ToLower(isListRaw) == "true" {
				isList = true
			}
			conditions = append(conditions, internal.Condition{
				Field:  fieldName,
				Op:     operator,
				Value:  value,
				IsList: isList,
			})
		}
	}
	return conditions, nil
}

func propertyMatches(property lib.Property, conditions []internal.Condition) bool {
	for _, c := range conditions {
		if !internal.CheckPropCondition(property, c) {
			return false
		}
	}
	return true
}
