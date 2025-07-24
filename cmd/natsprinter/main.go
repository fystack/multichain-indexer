package main

import (
	"fmt"
	"log"
	"os"

	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	subject := "blockchain.indexer.>"

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	fmt.Printf("Subscribed to subject: %s\n", subject)

	_, err = nc.Subscribe(subject, func(msg *nats.Msg) {
		fmt.Printf("[%s] %s\n", msg.Subject, string(msg.Data))
	})
	if err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	select {} // Block forever
}
