package mysql

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func TestUnsupportedHAEndpointProviderFailsClosed(t *testing.T) {
	provider := UnsupportedHAEndpointProvider{}
	resolved := adapter.ResolvedOperation{Target: model.DatabaseInstance{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}}}
	if provider.Executable(context.Background()) {
		t.Fatal("unsupported endpoint provider advertised execution")
	}
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("unsupported provider did not return a blocking check: %+v", checks)
	}
	if err := provider.Transfer(context.Background(), resolved); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("unsupported provider transfer error=%v", err)
	}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckFail {
		t.Fatalf("unsupported provider verification was not blocking: %+v", check)
	}
}
