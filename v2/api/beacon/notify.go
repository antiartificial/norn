package beacon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// Notifier dispatches beacon events to configured notification channels.
type Notifier struct {
	db     *store.DB
	client *http.Client
}

// NewNotifier creates a Notifier backed by the given database and a default HTTP client.
func NewNotifier(db *store.DB) *Notifier {
	return &Notifier{
		db: db,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Dispatch loads all notification channels, filters by severity, and sends the
// event to each matching channel asynchronously.
func (n *Notifier) Dispatch(ctx context.Context, event model.BeaconEvent) {
	channels, err := n.db.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("notifier: list channels: %v", err)
		return
	}

	for _, ch := range channels {
		if !channelMatchesSeverity(ch, string(event.Severity)) {
			continue
		}
		ch := ch
		go n.send(context.Background(), ch, event)
	}
}

// SendToChannel sends a single event to a specific channel (used for testing).
func (n *Notifier) SendToChannel(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	return n.send(ctx, ch, event)
}

func channelMatchesSeverity(ch model.NotificationChannel, severity string) bool {
	if len(ch.Severities) == 0 {
		return true
	}
	for _, s := range ch.Severities {
		if s == severity {
			return true
		}
	}
	return false
}

func (n *Notifier) send(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	var err error
	switch ch.Provider {
	case "discord":
		err = n.sendDiscord(ctx, ch, event)
	case "ntfy":
		err = n.sendNtfy(ctx, ch, event)
	case "pushover":
		err = n.sendPushover(ctx, ch, event)
	case "webhook":
		err = n.sendWebhook(ctx, ch, event)
	default:
		err = fmt.Errorf("unknown provider %q", ch.Provider)
	}
	if err != nil {
		log.Printf("notifier: send %s/%s for event %s: %v", ch.Provider, ch.Name, event.ID, err)
	}
	return err
}

func (n *Notifier) sendDiscord(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       event.Title,
				"description": event.Body,
				"color":       colorForSeverity(event.Severity),
				"fields": []map[string]string{
					{"name": "App", "value": event.App},
					{"name": "Type", "value": event.Type},
					{"name": "Severity", "value": string(event.Severity)},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord delivery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned %s", resp.Status)
	}
	return nil
}

func (n *Notifier) sendNtfy(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, strings.NewReader(event.Body))
	if err != nil {
		return fmt.Errorf("create ntfy request: %w", err)
	}
	req.Header.Set("X-Title", event.Title)
	req.Header.Set("X-Priority", ntfyPriority(event.Severity))

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy delivery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}

func (n *Notifier) sendPushover(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	form := url.Values{}
	form.Set("token", ch.Token)
	form.Set("user", ch.UserKey)
	form.Set("title", event.Title)
	form.Set("message", event.Body)
	form.Set("priority", pushoverPriority(event.Severity))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushover.net/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create pushover request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("pushover delivery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pushover returned %s", resp.Status)
	}
	return nil
}

func (n *Notifier) sendWebhook(ctx context.Context, ch model.NotificationChannel, event model.BeaconEvent) error {
	payload := map[string]interface{}{
		"id":       event.ID,
		"app":      event.App,
		"type":     event.Type,
		"severity": event.Severity,
		"title":    event.Title,
		"body":     event.Body,
		"metadata": event.Metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ch.Token)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook delivery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

func colorForSeverity(severity model.BeaconSeverity) int {
	switch severity {
	case model.BeaconCritical:
		return 0xFF0000
	case model.BeaconWarning:
		return 0xFFA500
	default:
		return 0x00FF00
	}
}

func ntfyPriority(severity model.BeaconSeverity) string {
	switch severity {
	case model.BeaconCritical:
		return "urgent"
	case model.BeaconWarning:
		return "high"
	default:
		return "default"
	}
}

func pushoverPriority(severity model.BeaconSeverity) string {
	switch severity {
	case model.BeaconCritical:
		return "1"
	case model.BeaconWarning:
		return "0"
	default:
		return "-1"
	}
}
