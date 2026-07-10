package adapter

import (
	"fmt"
	"sort"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

type Registry struct {
	mu       sync.RWMutex
	adapters map[model.Engine]DatabaseHAAdapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[model.Engine]DatabaseHAAdapter{}}
}

func (registry *Registry) Register(candidate DatabaseHAAdapter) error {
	if candidate == nil || !candidate.Engine().Valid() {
		return fmt.Errorf("invalid database adapter")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.adapters[candidate.Engine()]; exists {
		return fmt.Errorf("adapter already registered for %s", candidate.Engine())
	}
	registry.adapters[candidate.Engine()] = candidate
	return nil
}

func (registry *Registry) Get(engine model.Engine) (DatabaseHAAdapter, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	candidate, ok := registry.adapters[engine]
	return candidate, ok
}

func (registry *Registry) Engines() []model.Engine {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	engines := make([]model.Engine, 0, len(registry.adapters))
	for engine := range registry.adapters {
		engines = append(engines, engine)
	}
	sort.Slice(engines, func(i int, j int) bool { return engines[i] < engines[j] })
	return engines
}
