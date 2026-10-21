package main

import (
	"path/filepath"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/logcollect"
)

func TestConfigureLokiShipping(t *testing.T) {
	spool, err := logcollect.OpenSpool(filepath.Join(t.TempDir(), "spool"), logcollect.Limits{TotalBytes: 1 << 20, StreamBytes: 1 << 19, SegmentBytes: 1 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if shipper, err := configureLokiShipping(&config.Config{}, spool); shipper != nil || err != nil {
		t.Errorf("unset URL = %v %v", shipper, err)
	}
	if _, err := configureLokiShipping(&config.Config{LokiURL: "http://loki:3100/push"}, spool); err == nil {
		t.Error("URL with a path must fail fast")
	}
	if _, err := configureLokiShipping(&config.Config{LokiURL: "loki:3100"}, nil); err == nil {
		t.Error("an invalid URL must fail even without a spool")
	}
	if shipper, err := configureLokiShipping(&config.Config{LokiURL: "http://loki:3100"}, nil); shipper != nil || err != nil {
		t.Errorf("no spool = %v %v", shipper, err)
	}
	shipper, err := configureLokiShipping(&config.Config{LokiURL: "http://loki:3100", LokiPushInterval: time.Second}, spool)
	if err != nil || shipper == nil || shipper.MaxBatchLines != 5000 {
		t.Errorf("configured = %+v %v", shipper, err)
	}
}
