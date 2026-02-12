package cron

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"norn/api/hub"
	"norn/api/model"
	"norn/api/runtime"
	"norn/api/store"
)

type Scheduler struct {
	cron    *cron.Cron
	runner  runtime.Runner
	db      *store.DB
	ws      *hub.Hub
	specs   map[string]*model.InfraSpec // app → spec
	entries map[string]cron.EntryID     // app → cron entry ID
	images  map[string]string           // app → latest image tag
	mu      sync.Mutex
}

func New(runner runtime.Runner, db *store.DB, ws *hub.Hub) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
		runner:  runner,
		db:      db,
		ws:      ws,
		specs:   make(map[string]*model.InfraSpec),
		entries: make(map[string]cron.EntryID),
		images:  make(map[string]string),
	}
}

func (s *Scheduler) Start() {
	s.cron.Start()
	log.Println("cron: scheduler started")
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	log.Println("cron: scheduler stopped")
}

func (s *Scheduler) Sync(specs []*model.InfraSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	active := make(map[string]bool)

	for _, spec := range specs {
		if !spec.IsCron() || spec.Schedule == "" {
			continue
		}
		active[spec.App] = true
		s.specs[spec.App] = spec

		// Check for DB override
		schedule := spec.Schedule
		state, err := s.db.GetCronState(context.Background(), spec.App)
		if err != nil {
			log.Printf("cron: get state for %s: %v", spec.App, err)
		}
		if state != nil && state.Schedule != "" {
			schedule = state.Schedule
		}

		paused := false
		if state != nil {
			paused = state.Paused
		}

		// Persist state
		if err := s.db.UpsertCronState(context.Background(), spec.App, schedule, paused); err != nil {
			log.Printf("cron: upsert state for %s: %v", spec.App, err)
		}

		// Set default image if not yet known
		if _, ok := s.images[spec.App]; !ok {
			s.images[spec.App] = spec.App + ":latest"
		}

		// Add or update cron entry
		s.addOrUpdate(spec.App, schedule, paused)
	}

	// Remove entries for apps no longer in specs
	for app, entryID := range s.entries {
		if !active[app] {
			s.cron.Remove(entryID)
			delete(s.entries, app)
			delete(s.specs, app)
			delete(s.images, app)
		}
	}
}

func (s *Scheduler) addOrUpdate(app, schedule string, paused bool) {
	// Remove existing entry if any
	if entryID, ok := s.entries[app]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, app)
	}

	if paused {
		log.Printf("cron: %s is paused, skipping schedule", app)
		return
	}

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.execute(app)
	})
	if err != nil {
		log.Printf("cron: failed to schedule %s with '%s': %v", app, schedule, err)
		return
	}
	s.entries[app] = entryID

	// Update next run time
	entry := s.cron.Entry(entryID)
	if !entry.Next.IsZero() {
		s.db.UpdateCronNextRun(context.Background(), app, entry.Next)
	}

	log.Printf("cron: scheduled %s with '%s', next run: %s", app, schedule, entry.Next.Format(time.RFC3339))
}

func (s *Scheduler) execute(app string) {
	s.mu.Lock()
	spec := s.specs[app]
	imageTag := s.images[app]
	s.mu.Unlock()

	if spec == nil {
		log.Printf("cron: execute %s: spec not found", app)
		return
	}

	// Check if paused
	state, _ := s.db.GetCronState(context.Background(), app)
	if state != nil && state.Paused {
		return
	}

	timeout := time.Duration(spec.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	// Parse command
	var command []string
	if spec.Command != "" {
		command = []string{"sh", "-c", spec.Command}
	}

	exec := &model.CronExecution{
		App:       app,
		ImageTag:  imageTag,
		Status:    model.CronRunning,
		StartedAt: time.Now(),
	}

	id, err := s.db.InsertCronExecution(context.Background(), exec)
	if err != nil {
		log.Printf("cron: insert execution for %s: %v", app, err)
		return
	}
	exec.ID = id

	s.ws.Broadcast(hub.Event{Type: "cron.started", AppID: app, Payload: map[string]interface{}{
		"executionId": id,
		"imageTag":    imageTag,
	}})

	result, runErr := s.runner.Run(context.Background(), runtime.RunOpts{
		Image:   imageTag,
		Command: command,
		Env:     spec.Env,
		Timeout: timeout,
	})

	var status model.CronExecStatus
	var exitCode int
	var output string
	var durationMs int64

	if result != nil {
		exitCode = result.ExitCode
		output = result.Output
		durationMs = result.Duration.Milliseconds()
	}

	if runErr != nil {
		if strings.Contains(runErr.Error(), "timed out") {
			status = model.CronTimedOut
		} else {
			status = model.CronFailed
			output = fmt.Sprintf("%s\n%s", output, runErr.Error())
		}
	} else if exitCode != 0 {
		status = model.CronFailed
	} else {
		status = model.CronSucceeded
	}

	s.db.UpdateCronExecution(context.Background(), id, status, exitCode, output, durationMs)

	eventType := "cron.completed"
	if status == model.CronFailed || status == model.CronTimedOut {
		eventType = "cron.failed"
	}
	s.ws.Broadcast(hub.Event{Type: eventType, AppID: app, Payload: map[string]interface{}{
		"executionId": id,
		"status":      string(status),
		"exitCode":    exitCode,
		"durationMs":  durationMs,
	}})

	// Update next run time
	s.mu.Lock()
	if entryID, ok := s.entries[app]; ok {
		entry := s.cron.Entry(entryID)
		if !entry.Next.IsZero() {
			s.db.UpdateCronNextRun(context.Background(), app, entry.Next)
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) UpdateSchedule(app, schedule string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(schedule); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	state, _ := s.db.GetCronState(context.Background(), app)
	paused := false
	if state != nil {
		paused = state.Paused
	}

	if err := s.db.UpsertCronState(context.Background(), app, schedule, paused); err != nil {
		return err
	}

	s.addOrUpdate(app, schedule, paused)
	return nil
}

func (s *Scheduler) Pause(app string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, _ := s.db.GetCronState(context.Background(), app)
	schedule := ""
	if state != nil {
		schedule = state.Schedule
	}

	if err := s.db.UpsertCronState(context.Background(), app, schedule, true); err != nil {
		return err
	}

	// Remove the cron entry
	if entryID, ok := s.entries[app]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, app)
	}

	log.Printf("cron: paused %s", app)
	return nil
}

func (s *Scheduler) Resume(app string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, _ := s.db.GetCronState(context.Background(), app)
	schedule := ""
	if state != nil {
		schedule = state.Schedule
	}
	if schedule == "" {
		if spec, ok := s.specs[app]; ok {
			schedule = spec.Schedule
		}
	}
	if schedule == "" {
		return fmt.Errorf("no schedule for %s", app)
	}

	if err := s.db.UpsertCronState(context.Background(), app, schedule, false); err != nil {
		return err
	}

	s.addOrUpdate(app, schedule, false)
	log.Printf("cron: resumed %s", app)
	return nil
}

func (s *Scheduler) Trigger(app string) error {
	go s.execute(app)
	return nil
}

func (s *Scheduler) SetImage(app, imageTag string) {
	s.mu.Lock()
	s.images[app] = imageTag
	s.mu.Unlock()
}

func (s *Scheduler) GetState(app string) *model.CronState {
	state, _ := s.db.GetCronState(context.Background(), app)
	if state == nil {
		return nil
	}
	// Update next run from live entry if available
	s.mu.Lock()
	if entryID, ok := s.entries[app]; ok {
		entry := s.cron.Entry(entryID)
		if !entry.Next.IsZero() {
			state.NextRunAt = &entry.Next
		}
	}
	s.mu.Unlock()
	return state
}
