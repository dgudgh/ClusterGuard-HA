package mysql

import (
	"context"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type MaintenanceStore interface {
	SetMaintenance(context.Context, model.ResourceID, model.ResourceID, bool) error
	Maintenance(context.Context, model.ResourceID, model.ResourceID) (bool, error)
}

type UnsupportedMaintenanceStore struct{}

func (UnsupportedMaintenanceStore) SetMaintenance(context.Context, model.ResourceID, model.ResourceID, bool) error {
	return adapter.ErrUnsupported
}

func (UnsupportedMaintenanceStore) Maintenance(context.Context, model.ResourceID, model.ResourceID) (bool, error) {
	return false, adapter.ErrUnsupported
}
