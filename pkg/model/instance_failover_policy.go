package model

import "time"

const DefaultFailoverWeight = 100
const MaximumFailoverWeight = 1000

// Absence preserves the startup default. Explicit false is manual-only.
// Preferences belong to database instance UUIDs, never host/controller IDs.
type InstanceFailoverPolicy struct {
	Revision         uint64                                    `json:"revision"`
	AutomaticEnabled bool                                      `json:"automatic_enabled"`
	Instances        map[ResourceID]InstanceFailoverPreference `json:"instances"`
	UpdatedAt        time.Time                                 `json:"updated_at,omitempty"`
	UpdatedBy        string                                    `json:"updated_by,omitempty"`
}
type InstanceFailoverPreference struct {
	AutomaticEligible bool `json:"automatic_eligible"`
	Weight            int  `json:"weight"`
}

func (p *InstanceFailoverPolicy) Preference(id ResourceID) InstanceFailoverPreference {
	if p != nil {
		if v, ok := p.Instances[id]; ok {
			return v
		}
		return InstanceFailoverPreference{Weight: DefaultFailoverWeight}
	}
	return InstanceFailoverPreference{AutomaticEligible: true, Weight: DefaultFailoverWeight}
}
func (p *InstanceFailoverPolicy) Clone() *InstanceFailoverPolicy {
	if p == nil {
		return nil
	}
	q := *p
	q.Instances = make(map[ResourceID]InstanceFailoverPreference, len(p.Instances))
	for id, v := range p.Instances {
		q.Instances[id] = v
	}
	return &q
}
