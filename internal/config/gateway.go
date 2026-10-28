package config

import "clusterguard.io/ha/internal/endpoint"

// Certificate and key references are local bootstrap configuration, never
// browser-supplied. Each service endpoint selects a trusted profile by name.
type Gateway struct {
	Enabled  bool                               `json:"enabled"`
	Profiles map[string]endpoint.GatewayProfile `json:"profiles"`
}
