package main

import (
	"fmt"
	"log"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/logcollect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// configureLogCollection opens the diagnostic log spool and its collector
// when NORN_LOG_SPOOL_DIR is set. The spool is separate from evidence: its
// loss is counted, never reserved. Without Nomad the spool still serves
// what was collected.
func configureLogCollection(cfg *config.Config, client *nomad.Client) (*logcollect.Spool, *logcollect.Collector, error) {
	if cfg.LogSpoolDir == "" {
		return nil, nil, nil
	}
	spool, err := logcollect.OpenSpool(cfg.LogSpoolDir, logcollect.Limits{TotalBytes: cfg.LogSpoolMaxBytes, StreamBytes: cfg.LogSpoolStreamMaxBytes, SegmentBytes: cfg.LogSpoolSegmentBytes})
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		return spool, nil, nil
	}
	appsDir := cfg.AppsDir
	collector := &logcollect.Collector{Source: logcollect.NomadSource{Client: client}, Spool: spool, MaxFollowers: 256, Jobs: func() []logcollect.Job {
		specs, err := model.DiscoverApps(appsDir)
		if err != nil {
			log.Printf("log collection: discover apps: %v", err)
			return nil
		}
		var jobs []logcollect.Job
		for _, spec := range specs {
			for _, jobID := range nomad.JobIDs(spec) {
				jobs = append(jobs, logcollect.Job{App: spec.App, JobID: jobID})
			}
		}
		return jobs
	}}
	return spool, collector, nil
}

// configureLokiShipping validates NORN_LOKI_URL and returns the shipper that
// copies the spool to Loki. It is nil unless both the spool and the URL are
// configured; the URL is validated whenever it is set (queries use it too).
func configureLokiShipping(cfg *config.Config, spool *logcollect.Spool) (*logcollect.LokiShipper, error) {
	if cfg.LokiURL == "" {
		return nil, nil
	}
	client, err := logcollect.NewLokiClient(cfg.LokiURL, cfg.LokiTenant, cfg.LokiToken)
	if err != nil {
		return nil, fmt.Errorf("NORN_LOKI_URL: %w", err)
	}
	if spool == nil {
		return nil, nil
	}
	return &logcollect.LokiShipper{Spool: spool, Client: client, Interval: cfg.LokiPushInterval, MaxBackoff: time.Minute, MaxBatchBytes: 1 << 20, MaxBatchLines: 5000}, nil
}
