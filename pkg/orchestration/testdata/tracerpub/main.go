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
	topicID := os.Getenv("TOPIC_ID")
	client, _ := pubsub.NewClient(context.Background(), projectID)
	topic := client.Topic(topicID)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		traceID := r.URL.Query().Get("trace_id")
		topic.Publish(context.Background(), &pubsub.Message{Data: []byte(traceID)})
		fmt.Fprintf(w, "Published: %s", traceID)
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
