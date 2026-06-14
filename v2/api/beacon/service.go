package beacon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type Config struct {
	Environment string
	SinkURL     string
	SinkKeyID   string
	SinkSecret  string
}

type Service struct {
	db       *store.DB
	ws       *hub.Hub
	cfg      Config
	client   *http.Client
	notifier *Notifier
}

func New(db *store.DB, ws *hub.Hub, cfg Config) *Service {
	return &Service{
		db:  db,
		ws:  ws,
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SetNotifier attaches a Notifier to the service so that emitted events are
// dispatched to configured notification channels.
func (s *Service) SetNotifier(n *Notifier) {
	s.notifier = n
}

func (s *Service) SinkStatus() model.BeaconSinkStatus {
	return model.BeaconSinkStatus{
		Configured: s.cfg.SinkURL != "",
		URL:        s.cfg.SinkURL,
		KeyID:      s.cfg.SinkKeyID,
	}
}

func (s *Service) Emit(ctx context.Context, event model.BeaconEvent) (*model.BeaconEvent, error) {
	if event.ID == "" {
		event.ID = "evt_" + uuid.NewString()
	}
	if event.Source == "" {
		event.Source = "norn"
	}
	if event.Environment == "" {
		event.Environment = s.cfg.Environment
	}
	if event.Severity == "" {
		event.Severity = model.BeaconInfo
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.DedupeKey == "" && event.App != "" && event.Type != "" {
		event.DedupeKey = fmt.Sprintf("%s:%s", event.App, event.Type)
	}
	if event.Metadata == nil {
		event.Metadata = map[string]interface{}{}
	}

	if err := s.db.InsertBeaconEvent(ctx, &event); err != nil {
		return nil, err
	}

	if s.ws != nil {
		s.ws.Broadcast(hub.Event{Type: "beacon.event", AppID: event.App, Payload: event})
	}

	if s.cfg.SinkURL != "" {
		go s.forward(context.Background(), event)
	}

	if s.notifier != nil {
		go s.notifier.Dispatch(context.Background(), event)
	}

	return &event, nil
}

func (s *Service) forward(ctx context.Context, event model.BeaconEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("beacon: marshal sink event %s: %v", event.ID, err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.SinkURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("beacon: create sink request %s: %v", event.ID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beacon-Source", "norn")

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if s.cfg.SinkKeyID != "" {
		req.Header.Set("X-Vigil-Key-Id", s.cfg.SinkKeyID)
	}
	req.Header.Set("X-Vigil-Timestamp", timestamp)
	if s.cfg.SinkSecret != "" {
		req.Header.Set("X-Vigil-Signature", sign(s.cfg.SinkSecret, timestamp, body))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("beacon: sink delivery failed for %s: %v", event.ID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("beacon: sink delivery for %s returned %s", event.ID, resp.Status)
	}
}

func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
