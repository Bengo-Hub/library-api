package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/bengobox/library-service/internal/config"
)

// Connect opens a resilient NATS connection (infinite reconnect).
func Connect(cfg config.EventsConfig) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("library-api"),
		nats.Timeout(5 * time.Second),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
	}

	return nats.Connect(cfg.NATSURL, opts...)
}

// EnsureStream creates/updates the library JetStream stream that carries all
// library.* domain events (subjects follow {aggregate_type}.{event_type}; the
// aggregate_type for this service is always "library").
func EnsureStream(ctx context.Context, nc *nats.Conn, cfg config.EventsConfig) error {
	if nc == nil {
		return fmt.Errorf("nats connection is nil")
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream init: %w", err)
	}

	desiredSubjects := []string{"library.>"}

	info, err := js.StreamInfo(cfg.StreamName)
	if err == nil {
		subjectsDiffer := len(info.Config.Subjects) != len(desiredSubjects) || info.Config.Subjects[0] != desiredSubjects[0]
		if subjectsDiffer || info.Config.MaxAge != streamMaxAge {
			info.Config.Subjects = desiredSubjects
			info.Config.MaxAge = streamMaxAge
			if _, updateErr := js.UpdateStream(&info.Config); updateErr != nil {
				return fmt.Errorf("update stream: %w", updateErr)
			}
		}
		return nil
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     cfg.StreamName,
		Subjects: desiredSubjects,
		Replicas: 1,
		MaxAge:   streamMaxAge,
	})
	return err
}

// streamMaxAge bounds how long events stay in the stream, like the other service streams (7
// days). Events are kept after consumers ack them (several services read the same event), so
// without an age limit the stream grew forever: it had kept every event since July 2026.
const streamMaxAge = 7 * 24 * time.Hour
