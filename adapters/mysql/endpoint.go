package mysql

import (
	"context"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type UnsupportedHAEndpointProvider struct{}

func (UnsupportedHAEndpointProvider) Executable(context.Context) bool { return false }

func (UnsupportedHAEndpointProvider) Precheck(context.Context, adapter.ResolvedOperation) []model.Check {
	return []model.Check{{
		Name:    "writer_endpoint_provider",
		Status:  model.CheckFail,
		Message: "a real writer-endpoint provider is required before planned switchover execution",
	}}
}

func (UnsupportedHAEndpointProvider) Transfer(context.Context, adapter.ResolvedOperation) error {
	return adapter.ErrUnsupported
}

func (UnsupportedHAEndpointProvider) Verify(context.Context, adapter.ResolvedOperation) model.Check {
	return model.Check{
		Name:    "writer_endpoint_owner",
		Status:  model.CheckFail,
		Message: "writer-endpoint ownership cannot be verified without a configured provider",
	}
}
