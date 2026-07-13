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
	writerendpoint "clusterguard.io/ha/internal/endpoint"
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
	mysqlAdapter := mysql.New(mysql.CLIQueryRunner{})
	if configuration.Agent.Enabled {
		transport, transportErr := writerendpoint.NewSSHAgentTransport(writerendpoint.SSHAgentTransportConfig{
			SSHBinary: configuration.Agent.SSHBinary, User: configuration.Agent.User,
			IdentityFile: configuration.Agent.IdentityFile, KnownHostsFile: configuration.Agent.KnownHostsFile,
			AgentBinary: configuration.Agent.AgentBinary, AgentConfigPath: configuration.Agent.AgentConfigPath,
		}, writerendpoint.OSProcessRunner{})
		if transportErr != nil {
			return nil, fmt.Errorf("configure agent transport: %w", transportErr)
		}
		provider := writerendpoint.NewLinuxVIPProvider(repository, transport, writerendpoint.NewMemoryLeaseStore(nil), configuration.Agent.SharedSecret, nil)
		mysqlAdapter = mysql.NewWithEndpointProvider(mysql.CLIQueryRunner{}, provider)
	}
	for _, candidate := range []adapter.DatabaseHAAdapter{
		mysqlAdapter,
		postgresql.New(),
		oracle.New(),
		sqlserver.New(),
	} {
		if err := registry.Register(candidate); err != nil {
			return nil, fmt.Errorf("register %s adapter: %w", candidate.Engine(), err)
		}
	}
	locks := workflow.NewMemoryLocks()
	mysqlCredentials := func(context.Context, model.DatabaseCluster) (adapter.OperationCredentials, error) {
		return mysqlOperationCredentials(configuration.MySQL)
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
		return mysqlDiscoveryCredentials(configuration.MySQL)
	}), nil, discovery.WithPublicationFence(locks))
	return api.NewServer(registry, repository, service, refresher, api.WithControlToken(configuration.ControlToken)), nil
}

func mysqlOperationCredentials(configuration config.MySQL) (adapter.OperationCredentials, error) {
	if !configuration.Enabled {
		return adapter.OperationCredentials{}, fmt.Errorf("MySQL operation credentials are not configured")
	}
	if configuration.Operation.Username == "" || configuration.Operation.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("MySQL operation credentials are incomplete")
	}
	if configuration.Replication.Username == "" || configuration.Replication.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("MySQL replication credentials are incomplete")
	}
	return adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: configuration.Operation.Username, Password: configuration.Operation.Password},
		Replication:    adapter.Credentials{Username: configuration.Replication.Username, Password: configuration.Replication.Password},
	}, nil
}

func mysqlDiscoveryCredentials(configuration config.MySQL) (adapter.Credentials, error) {
	if !configuration.Enabled || configuration.Discovery.Username == "" || configuration.Discovery.Password == "" {
		return adapter.Credentials{}, fmt.Errorf("MySQL discovery credentials are not configured")
	}
	return adapter.Credentials{Username: configuration.Discovery.Username, Password: configuration.Discovery.Password}, nil
}
