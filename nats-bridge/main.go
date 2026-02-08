package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	natsURL := getEnv("NATS_URL", "nats://nats.roundtable.svc:4222")
	topics := getEnv("SUBSCRIBE_TOPICS", "")
	webhookURL := getEnv("OPENCLAW_WEBHOOK_URL", "http://localhost:18789/webhook")
	resultSuffix := getEnv("RESULT_TOPIC_SUFFIX", ".result")
	healthPort := getEnv("HEALTH_PORT", "8080")

	if topics == "" {
		log.Fatal("SUBSCRIBE_TOPICS is required")
	}

	// Connect to NATS
	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("NATS disconnected: %v", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Println("NATS reconnected")
		}),
	)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()
	log.Printf("Connected to NATS at %s", natsURL)

	// HTTP client for webhook calls
	client := &http.Client{Timeout: 120 * time.Second}

	// Subscribe to each topic
	var subs []*nats.Subscription
	for _, topic := range strings.Split(topics, ",") {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}

		resultTopic := topic + resultSuffix
		sub, err := nc.Subscribe(topic, func(msg *nats.Msg) {
			log.Printf("Received message on %s (%d bytes)", msg.Subject, len(msg.Data))

			// POST to OpenClaw webhook
			resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(msg.Data))
			if err != nil {
				log.Printf("Webhook POST failed: %v", err)
				publishError(nc, resultTopic, msg.Subject, err)
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("Failed to read webhook response: %v", err)
				return
			}

			log.Printf("Webhook responded %d, publishing to %s", resp.StatusCode, resultTopic)
			if err := nc.Publish(resultTopic, body); err != nil {
				log.Printf("Failed to publish result: %v", err)
			}
		})
		if err != nil {
			log.Fatalf("Failed to subscribe to %s: %v", topic, err)
		}
		subs = append(subs, sub)
		log.Printf("Subscribed to %s (results → %s)", topic, resultTopic)
	}

	// Health check server
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !nc.IsConnected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "nats disconnected")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	srv := &http.Server{Addr: ":" + healthPort, Handler: mux}
	go func() {
		log.Printf("Health check listening on :%s/healthz", healthPort)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Health server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")

	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	_ = nc.Drain()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)

	log.Println("Goodbye.")
}

func publishError(nc *nats.Conn, topic, source string, origErr error) {
	errMsg, _ := json.Marshal(map[string]string{
		"error":  origErr.Error(),
		"source": source,
	})
	_ = nc.Publish(topic, errMsg)
}
