package dispatch

import (
	"sync"
	"time"
)

type Worker struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	Capabilities  []string   `json:"capabilities"`
	TasksActive   int        `json:"tasksActive"`
	MaxConcurrent int        `json:"maxConcurrent"`
	LastSeenAt    *time.Time `json:"lastSeenAt,omitempty"`
	Draining      bool       `json:"draining"`
}

type Registry struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

func NewRegistry() *Registry {
	return &Registry{
		workers: make(map[string]*Worker),
	}
}

func (r *Registry) List() []*Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workers := make([]*Worker, 0, len(r.workers))
	for _, worker := range r.workers {
		copy := *worker
		workers = append(workers, &copy)
	}
	return workers
}

func (r *Registry) Get(id string) *Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	worker := r.workers[id]
	if worker == nil {
		return nil
	}
	copy := *worker
	return &copy
}

func (r *Registry) Drain(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker := r.workers[id]
	if worker == nil {
		return false
	}
	worker.Draining = true
	worker.Status = "draining"
	return true
}

func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, id)
}
