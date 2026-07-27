package oracle

import (
	"context"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type BrokerControlStatus struct {
	Database            string
	Role                string
	ConfigurationStatus string
	ReadyForSwitchover  bool
	TransportLagSeconds *int64
	ApplyLagSeconds     *int64
}

type BrokerController interface {
	Executable(context.Context) bool
	Precheck(context.Context, adapter.ResolvedOperation) []model.Check
	Switchover(context.Context, adapter.ResolvedOperation, model.ResourceID) error
	Status(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, string) (BrokerControlStatus, error)
}

type UnsupportedBrokerController struct{}

func (UnsupportedBrokerController) Executable(context.Context) bool { return false }
func (UnsupportedBrokerController) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{
		Name: "oracle_broker_controller", Status: model.CheckFail,
		Message: "restricted Oracle Data Guard Broker control is not configured",
	}}
}
func (UnsupportedBrokerController) Switchover(context.Context, adapter.ResolvedOperation, model.ResourceID) error {
	return adapter.ErrUnsupported
}
func (UnsupportedBrokerController) Status(context.Context, adapter.ResolvedOperation, model.DatabaseInstance, string) (BrokerControlStatus, error) {
	return BrokerControlStatus{}, adapter.ErrUnsupported
}

type UnsupportedHAEndpointProvider struct{}

func (UnsupportedHAEndpointProvider) Executable(context.Context) bool { return false }
func (UnsupportedHAEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{
		Name: "writer_endpoint_provider", Status: model.CheckFail,
		Message: "Oracle writer endpoint control is not configured",
	}}
}
func (UnsupportedHAEndpointProvider) AuthorizeTransition(context.Context, adapter.ResolvedOperation) (adapter.TransitionAuthorization, error) {
	return adapter.TransitionAuthorization{}, adapter.ErrUnsupported
}
func (UnsupportedHAEndpointProvider) Transfer(context.Context, adapter.ResolvedOperation) error {
	return adapter.ErrUnsupported
}
func (UnsupportedHAEndpointProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{Name: "writer_endpoint_owner", Status: model.CheckFail, Message: "Oracle writer endpoint control is not configured"}
}
