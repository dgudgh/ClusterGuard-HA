package sqlserver

import (
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Adapter struct{ adapter.UnsupportedAdapter }

func New() *Adapter {
	return &Adapter{UnsupportedAdapter: adapter.NewUnsupported(model.EngineSQLServer)}
}
