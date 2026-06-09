package dispatch

import "norn/api/store"

type Dispatcher struct {
	registry *Registry
	db       *store.DB
}

func NewDispatcher(registry *Registry, db *store.DB) *Dispatcher {
	return &Dispatcher{
		registry: registry,
		db:       db,
	}
}
