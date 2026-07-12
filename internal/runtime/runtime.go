// Package runtime wires independently designed platform components together.
package runtime

import (
	"context"
	"fmt"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/discovery"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func New(configuration config.File) (*api.Server, error) {
	repository, err := store.Open(configuration.MetadataPath)
	if err != nil {
		return nil, err
	}
	registry := adapter.NewRegistry()
	for _, candidate := range []adapter.DatabaseHAAdapter{
		mysql.New(mysql.CLIQueryRunner{}),
		postgresql.New(),
		oracle.New(),
		sqlserver.New(),
	} {
		if err := registry.Register(candidate); err != nil {
			return nil, fmt.Errorf("register %s adapter: %w", candidate.Engine(), err)
		}
	}
	locks := workflow.NewMemoryLocks()
	mysqlCredentials := func(context.Context, model.DatabaseCluster) (adapter.Credentials, error) {
		if !configuration.MySQL.Enabled {
			return adapter.Credentials{}, fmt.Errorf("MySQL operation credentials are not configured")
		}
		return adapter.Credentials{Username: configuration.MySQL.Username, Password: configuration.MySQL.Password}, nil
	}
	service := workflow.New(
		registry,
		workflow.TopologyDiscovery{Reader: repository},
		workflow.AllowAllSafety{},
		locks,
		workflow.TokenApproval{ExpectedToken: configuration.ApprovalToken},
		repository,
		workflow.WithOperationStore(repository),
		workflow.WithOperationResolver(workflow.RepositoryResolver{
			Reader:      repository,
			Credentials: workflow.CredentialProviderFunc(mysqlCredentials),
		}),
	)
	refresher := discovery.New(registry, repository, discovery.CredentialResolverFunc(func(context.Context, model.DatabaseCluster, model.Endpoint) (adapter.Credentials, error) {
		if !configuration.MySQL.Enabled {
			return adapter.Credentials{}, fmt.Errorf("MySQL discovery credentials are not configured")
		}
		return adapter.Credentials{Username: configuration.MySQL.Username, Password: configuration.MySQL.Password}, nil
	}), nil, discovery.WithPublicationFence(locks))
	return api.NewServer(registry, repository, service, refresher, api.WithControlToken(configuration.ControlToken)), nil
}
