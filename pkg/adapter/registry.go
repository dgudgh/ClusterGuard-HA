package adapter

import (
	"fmt"
	"sort"
	"sync"

	"clusterguard.io/ha/pkg/model"
)

// WildcardEngine registers an adapter that serves every engine. It is used for
// cross-engine operations such as power shutdown, where the behavior is
// engine-independent and registering per engine would collide with the
// database-specific adapters.
const WildcardEngine model.Engine = "*"

type Registry struct {
	mu       sync.RWMutex
	adapters map[model.Engine]DatabaseHAAdapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: map[model.Engine]DatabaseHAAdapter{}}
}

func (registry *Registry) Register(candidate DatabaseHAAdapter) error {
	if candidate == nil {
		return fmt.Errorf("invalid database adapter")
	}
	engine := candidate.Engine()
	if engine != WildcardEngine && !engine.Valid() {
		return fmt.Errorf("invalid database adapter engine %s", engine)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.adapters[engine]; exists {
		return fmt.Errorf("adapter already registered for %s", engine)
	}
	registry.adapters[engine] = candidate
	return nil
}

func (registry *Registry) Get(engine model.Engine) (DatabaseHAAdapter, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	candidate, ok := registry.adapters[engine]
	if ok {
		return candidate, true
	}
	candidate, ok = registry.adapters[WildcardEngine]
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
