// Package runtime wires independently designed platform components together.
package runtime

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/discovery"
	writerendpoint "clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Runtime struct {
	server    *api.Server
	consensus *consensus.Node
	runCtx    context.Context
	cancel    context.CancelFunc
	wait      sync.WaitGroup
}

func (runtime *Runtime) startLoop(run func(context.Context)) {
	if runtime.runCtx == nil {
		runtime.runCtx, runtime.cancel = context.WithCancel(context.Background())
	}
	runtime.wait.Add(1)
	go func() {
		defer runtime.wait.Done()
		run(runtime.runCtx)
	}()
}

func (runtime *Runtime) Handler() http.Handler {
	if runtime == nil || runtime.server == nil {
		return http.NotFoundHandler()
	}
	return runtime.server.Handler()
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	if runtime.cancel != nil {
		runtime.cancel()
		runtime.wait.Wait()
	}
	if runtime.consensus == nil {
		return nil
	}
	return runtime.consensus.Close()
}

func New(configuration config.File) (*Runtime, error) {
	repository, err := store.Open(configuration.MetadataPath)
	if err != nil {
		return nil, err
	}
	result := &Runtime{}
	if configuration.Consensus.Enabled {
		peers := make([]consensus.Peer, len(configuration.Consensus.Peers))
		for index, peer := range configuration.Consensus.Peers {
			peers[index] = consensus.Peer{ResourceID: peer.ResourceID, Address: peer.Address}
		}
		result.consensus, err = consensus.Open(consensus.Config{
			LocalID: configuration.Consensus.LocalID, BindAddress: configuration.Consensus.BindAddress,
			AdvertiseAddress: configuration.Consensus.AdvertiseAddress, DataDirectory: configuration.Consensus.DataDirectory,
			Peers: peers, Bootstrap: configuration.Consensus.Bootstrap,
			ApplyTimeout: time.Duration(configuration.Consensus.ApplyTimeoutSeconds) * time.Second,
		}, repository)
		if err != nil {
			return nil, fmt.Errorf("start controller consensus: %w", err)
		}
		if err := repository.SetSnapshotConsensus(result.consensus); err != nil {
			_ = result.Close()
			return nil, fmt.Errorf("attach replicated metadata store: %w", err)
		}
	}
	registry := adapter.NewRegistry()
	var endpointProvider adapter.HAEndpointProvider = mysql.UnsupportedHAEndpointProvider{}
	var vipProvider *writerendpoint.LinuxVIPProvider
	var ownershipLeases *coordination.LeaseStore
	if configuration.Agent.Enabled {
		if result.consensus == nil {
			return nil, fmt.Errorf("agent-backed VIP execution requires controller consensus")
		}
		transport, transportErr := writerendpoint.NewSSHAgentTransport(writerendpoint.SSHAgentTransportConfig{
			SSHBinary: configuration.Agent.SSHBinary, User: configuration.Agent.User,
			IdentityFile: configuration.Agent.IdentityFile, KnownHostsFile: configuration.Agent.KnownHostsFile,
			AgentBinary: configuration.Agent.AgentBinary, AgentConfigPath: configuration.Agent.AgentConfigPath,
		}, writerendpoint.OSProcessRunner{})
		if transportErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure agent transport: %w", transportErr)
		}
		ownershipLeases = coordination.NewLeaseStore(repository, result.consensus, nil)
		vipProvider = writerendpoint.NewLinuxVIPProvider(repository, transport, ownershipLeases, configuration.Agent.SharedSecret, nil)
		endpointProvider = vipProvider
	}
	mysqlAdapter := mysql.NewWithProviders(mysql.CLIQueryRunner{}, endpointProvider, repository)
	for _, candidate := range []adapter.DatabaseHAAdapter{
		mysqlAdapter,
		postgresql.New(),
		oracle.New(),
		sqlserver.New(),
	} {
		if err := registry.Register(candidate); err != nil {
			_ = result.Close()
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
	options := []api.ServerOption{api.WithControlToken(configuration.ControlToken), api.WithMonitoringToken(configuration.MonitoringToken)}
	if configuration.Agent.Enabled {
		options = append(options, api.WithAgentReconcileSecret(configuration.Agent.SharedSecret))
	}
	if result.consensus != nil {
		options = append(options, api.WithMutationAuthority(result.consensus))
	}
	if configuration.NodeLifecycle.Enabled {
		if result.consensus == nil {
			_ = result.Close()
			return nil, fmt.Errorf("node lifecycle execution requires controller consensus")
		}
		executor, executorErr := lifecycle.NewShellExecutor(configuration.NodeLifecycle.ExecutorPath, lifecycle.OSLifecycleProcessRunner{}, lifecycle.WithShellEnvironment(lifecycle.ShellEnvironment{
			PackageRepository: configuration.NodeLifecycle.PackageRepository,
			KnownHostsFile:    configuration.NodeLifecycle.KnownHostsFile,
			IdentityFile:      configuration.NodeLifecycle.IdentityFile,
			JQBinary:          configuration.NodeLifecycle.JQBinary,
			ControlJoinHelper: configuration.NodeLifecycle.ControlJoinHelper,
			CloneHelper:       configuration.NodeLifecycle.CloneHelper,
			XtraBackupHelper:  configuration.NodeLifecycle.XtraBackupHelper,
		}))
		if executorErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure node lifecycle executor: %w", executorErr)
		}
		manager := lifecycle.NewManager(repository, result.consensus, lifecycle.PlanSafetyGuard{}, locks, lifecycle.TokenApproval{ExpectedToken: configuration.ApprovalToken}, executor, repository, nil)
		secrets := nodeLifecycleSecrets(configuration.NodeLifecycle)
		options = append(options, api.WithNodeLifecycle(manager, nodeLifecycleCapabilities(configuration.NodeLifecycle), api.LifecycleSecretProviderFunc(func(context.Context, lifecycle.Request) (lifecycle.ExecutionSecrets, error) {
			return secrets, nil
		})))
	}
	result.server = api.NewServer(registry, repository, service, refresher, options...)
	if configuration.MySQL.Enabled && configuration.MySQL.DiscoveryIntervalSeconds > 0 {
		var authority discovery.ScheduledMutationAuthority
		if result.consensus != nil {
			authority = result.consensus
		}
		scheduler := discovery.NewScheduler(
			repository, refresher, authority,
			time.Duration(configuration.MySQL.DiscoveryIntervalSeconds)*time.Second,
			time.Duration(configuration.MySQL.DiscoveryTimeoutSeconds)*time.Second,
		)
		result.startLoop(scheduler.Run)
	}
	if vipProvider != nil && ownershipLeases != nil && result.consensus != nil {
		keeper := coordination.NewOwnershipKeeper(repository, vipProvider, ownershipLeases, result.consensus, nil, 5*time.Second, 15*time.Second)
		result.startLoop(keeper.Run)
	}
	return result, nil
}

func nodeLifecycleSecrets(configuration config.NodeLifecycle) lifecycle.ExecutionSecrets {
	return lifecycle.ExecutionSecrets{
		SSHPassword: configuration.SSHPassword, MySQLRootPassword: configuration.MySQLRootPassword,
		ReplicationPassword: configuration.ReplicationPassword,
	}
}

func nodeLifecycleCapabilities(configuration config.NodeLifecycle) lifecycle.Capabilities {
	versions := make(map[string]bool, len(configuration.XtraBackupVersions))
	for version, available := range configuration.XtraBackupVersions {
		versions[version] = available
	}
	return lifecycle.Capabilities{CloneAvailable: configuration.CloneAvailable, XtraBackupVersions: versions, LogicalDumpAllowed: configuration.LogicalDumpAllowed}
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
