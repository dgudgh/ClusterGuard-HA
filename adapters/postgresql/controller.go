package postgresql

import (
	"context"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type NodeController interface {
	Executable(context.Context) bool
	Precheck(context.Context, adapter.ResolvedOperation) []model.Check
	Status(context.Context, adapter.ResolvedOperation, model.DatabaseInstance) (bool, bool, error)
	Stop(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error
	IsStopped(context.Context, adapter.ResolvedOperation, model.DatabaseInstance) (bool, error)
	Promote(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error
	Repoint(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error
	Rewind(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error
	BaseBackup(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error
	Start(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error
}

type UnsupportedNodeController struct{}

func (UnsupportedNodeController) Executable(context.Context) bool { return false }
func (UnsupportedNodeController) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "postgresql_node_controller", Status: model.CheckFail, Message: "restricted PostgreSQL node control is not configured"}}
}
func (UnsupportedNodeController) Status(context.Context, adapter.ResolvedOperation, model.DatabaseInstance) (bool, bool, error) {
	return false, false, adapter.ErrUnsupported
}
func (UnsupportedNodeController) Stop(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedNodeController) IsStopped(context.Context, adapter.ResolvedOperation, model.DatabaseInstance) (bool, error) {
	return false, adapter.ErrUnsupported
}
func (UnsupportedNodeController) Promote(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedNodeController) Repoint(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedNodeController) Rewind(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedNodeController) BaseBackup(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedNodeController) Start(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, model.ResourceID) error {
	return adapter.ErrUnsupported
}

type FailoverSafetyProvider interface {
	Precheck(context.Context, adapter.ResolvedOperation) []model.Check
	Fence(context.Context, adapter.ResolvedOperation) error
	Verify(context.Context, adapter.ResolvedOperation) model.Check
}

type UnsupportedFailoverSafetyProvider struct{}

func (UnsupportedFailoverSafetyProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{
		{Name: "stable_primary_failure", Status: model.CheckFail, Message: "stable primary-failure observation is not configured"},
		{Name: "controller_quorum", Status: model.CheckFail, Message: "controller quorum is not configured"},
		{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"},
	}
}
func (UnsupportedFailoverSafetyProvider) Fence(context.Context, adapter.ResolvedOperation) error {
	return adapter.ErrUnsupported
}
func (UnsupportedFailoverSafetyProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "old_primary_fenced", Status: model.CheckFail, Message: "old-primary fencing is not configured"}
}

type UnsupportedHAEndpointProvider struct{}

func (UnsupportedHAEndpointProvider) Executable(context.Context) bool { return false }
func (UnsupportedHAEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{Name: "writer_endpoint_provider", Status: model.CheckFail, Message: "a real writer-endpoint provider is required"}}
}
func (UnsupportedHAEndpointProvider) AuthorizeTransition(context.Context, adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	return adapter.TransitionAuthorization{}, adapter.ErrUnsupported
}
func (UnsupportedHAEndpointProvider) Transfer(context.Context, adapter.ResolvedOperation) error {
	return adapter.ErrUnsupported
}
func (UnsupportedHAEndpointProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "writer-endpoint ownership is unavailable"}
}
