package main

import (
	"log"

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
