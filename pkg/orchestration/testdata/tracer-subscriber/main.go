package main

import (
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	projectID := os.Getenv("PROJECT_ID")
	subID := os.Getenv("SUBSCRIPTION_ID")
	client, _ := pubsub.NewClient(context.Background(), projectID)
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
		http.ListenAndServe(":"+os.Getenv("PORT"), nil)
	}()
	client.Subscription(subID).Receive(context.Background(), func(ctx context.Context, m *pubsub.Message) {
		log.Printf("Received tracer message: %s", string(m.Data))
		m.Ack()
	})
}
