// Package runtime wires independently designed platform components together.
package runtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/adapters/mysql"
	"clusterguard.io/ha/adapters/oracle"
	"clusterguard.io/ha/adapters/postgresql"
	"clusterguard.io/ha/adapters/sqlserver"
	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/approval"
	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/discovery"
	writerendpoint "clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/maintenance"
	"clusterguard.io/ha/internal/platformupdate"
	"clusterguard.io/ha/internal/recovery"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/internal/workflow"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type Runtime struct {
	server              *api.Server
	consensus           *consensus.Node
	authentication      *platformauth.Service
	automaticRecovery   *recovery.Controller
	automaticRecoveries []*recovery.Controller
	runCtx              context.Context
	cancel              context.CancelFunc
	wait                sync.WaitGroup
}

type engineClusterSource struct {
	source discovery.ScheduledClusterSource
	engine model.Engine
}

type consensusLifecycleMembership struct {
	node      *consensus.Node
	raftPort  string
	apiScheme string
	apiPort   string
}

func newConsensusLifecycleMembership(node *consensus.Node, configuration config.File) (*consensusLifecycleMembership, error) {
	if node == nil {
		return nil, fmt.Errorf("controller consensus is required")
	}
	_, raftPort, err := net.SplitHostPort(configuration.Consensus.AdvertiseAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve controller Raft port: %w", err)
	}
	_, apiPort, err := net.SplitHostPort(configuration.HTTPAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve controller API port: %w", err)
	}
	scheme := "http"
	if strings.TrimSpace(configuration.TLSCertFile) != "" {
		scheme = "https"
	}
	return &consensusLifecycleMembership{node: node, raftPort: raftPort, apiScheme: scheme, apiPort: apiPort}, nil
}

func (membership *consensusLifecycleMembership) AddControllers(ctx context.Context, targets []lifecycle.ControllerTarget) error {
	if membership == nil || membership.node == nil {
		return fmt.Errorf("controller membership integration is unavailable")
	}
	members := make([]consensus.ControllerMember, 0, len(targets))
	for _, target := range targets {
		host := strings.TrimSpace(target.IPAddress)
		if host == "" {
			host = strings.TrimSpace(target.Hostname)
		}
		if host == "" {
			return fmt.Errorf("controller %s has no network address", target.NodeName)
		}
		members = append(members, consensus.ControllerMember{
			ResourceID: target.ResourceID,
			Address:    net.JoinHostPort(host, membership.raftPort),
			APIAddress: membership.apiScheme + "://" + net.JoinHostPort(host, membership.apiPort),
		})
	}
	return membership.node.AddControllerVoters(ctx, members)
}

func (source engineClusterSource) Clusters() []model.DatabaseCluster {
	if source.source == nil || !source.engine.Valid() {
		return nil
	}
	clusters := source.source.Clusters()
	filtered := make([]model.DatabaseCluster, 0, len(clusters))
	for _, cluster := range clusters {
		if cluster.Engine == source.engine {
			filtered = append(filtered, cluster)
		}
	}
	return filtered
}

type engineSetClusterSource struct {
	source  discovery.ScheduledClusterSource
	engines []model.Engine
}

func (source engineSetClusterSource) Clusters() []model.DatabaseCluster {
	if source.source == nil || len(source.engines) == 0 {
		return nil
	}
	enabled := make(map[model.Engine]bool, len(source.engines))
	for _, engine := range source.engines {
		if engine.Valid() {
			enabled[engine] = true
		}
	}
	clusters := source.source.Clusters()
	filtered := make([]model.DatabaseCluster, 0, len(clusters))
	for _, cluster := range clusters {
		if enabled[cluster.Engine] {
			filtered = append(filtered, cluster)
		}
	}
	return filtered
}

type discoverySchedule struct {
	engines  []model.Engine
	interval time.Duration
	timeout  time.Duration
}

type discoveryScheduleKey struct {
	interval time.Duration
	timeout  time.Duration
}

func configuredDiscoverySchedules(configuration config.File) []discoverySchedule {
	grouped := make(map[discoveryScheduleKey][]model.Engine)
	add := func(enabled bool, engine model.Engine, intervalSeconds, timeoutSeconds int) {
		if !enabled || intervalSeconds <= 0 {
			return
		}
		timeout := time.Duration(timeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 4 * time.Second
		}
		key := discoveryScheduleKey{
			interval: time.Duration(intervalSeconds) * time.Second,
			timeout:  timeout,
		}
		grouped[key] = append(grouped[key], engine)
	}
	add(configuration.MySQL.Enabled, model.EngineMySQL, configuration.MySQL.DiscoveryIntervalSeconds, configuration.MySQL.DiscoveryTimeoutSeconds)
	add(configuration.PostgreSQL.Enabled, model.EnginePostgreSQL, configuration.PostgreSQL.DiscoveryIntervalSeconds, configuration.PostgreSQL.DiscoveryTimeoutSeconds)
	add(configuration.Oracle.Enabled, model.EngineOracle, configuration.Oracle.DiscoveryIntervalSeconds, configuration.Oracle.DiscoveryTimeoutSeconds)
	add(configuration.SQLServer.Enabled, model.EngineSQLServer, configuration.SQLServer.DiscoveryIntervalSeconds, configuration.SQLServer.DiscoveryTimeoutSeconds)

	schedules := make([]discoverySchedule, 0, len(grouped))
	for key, engines := range grouped {
		sort.Slice(engines, func(left, right int) bool { return engines[left] < engines[right] })
		schedules = append(schedules, discoverySchedule{engines: engines, interval: key.interval, timeout: key.timeout})
	}
	sort.Slice(schedules, func(left, right int) bool {
		if schedules[left].interval != schedules[right].interval {
			return schedules[left].interval < schedules[right].interval
		}
		if schedules[left].timeout != schedules[right].timeout {
			return schedules[left].timeout < schedules[right].timeout
		}
		return schedules[left].engines[0] < schedules[right].engines[0]
	})
	return schedules
}

func consensusPeerAPIAddress(configuration config.File, peer config.ConsensusPeer) (string, error) {
	if address := strings.TrimRight(strings.TrimSpace(peer.APIAddress), "/"); address != "" {
		return address, nil
	}
	httpAddress := strings.TrimSpace(configuration.HTTPAddress)
	if httpAddress == "" {
		httpAddress = "127.0.0.1:8088"
	}
	_, apiPort, err := net.SplitHostPort(httpAddress)
	if err != nil || apiPort == "" {
		return "", fmt.Errorf("derive controller API port from http_address %q", httpAddress)
	}
	leaderHost, _, err := net.SplitHostPort(strings.TrimSpace(peer.Address))
	if err != nil || strings.TrimSpace(leaderHost) == "" {
		return "", fmt.Errorf("derive controller API host from consensus peer %q", peer.Address)
	}
	scheme := "http"
	if strings.TrimSpace(configuration.TLSCertFile) != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(leaderHost, apiPort), nil
}

func consensusConfiguration(configuration config.File) (consensus.Config, error) {
	peers := make([]consensus.Peer, len(configuration.Consensus.Peers))
	for index, peer := range configuration.Consensus.Peers {
		apiAddress, err := consensusPeerAPIAddress(configuration, peer)
		if err != nil {
			return consensus.Config{}, fmt.Errorf("configure controller API address: %w", err)
		}
		peers[index] = consensus.Peer{ResourceID: peer.ResourceID, Address: peer.Address, APIAddress: apiAddress}
	}
	return consensus.Config{
		LocalID: configuration.Consensus.LocalID, BindAddress: configuration.Consensus.BindAddress,
		AdvertiseAddress: configuration.Consensus.AdvertiseAddress, DataDirectory: configuration.Consensus.DataDirectory,
		Peers: peers, Bootstrap: configuration.Consensus.Bootstrap,
		ApplyTimeout:                    time.Duration(configuration.Consensus.ApplyTimeoutSeconds) * time.Second,
		SnapshotCASEnabled:              configuration.Consensus.SnapshotCASEnabled,
		ReplicatedLogCompressionEnabled: configuration.Consensus.ReplicatedLogCompressionEnabled,
		AllowInsecureTransport:          configuration.Consensus.AllowInsecureTransport,
		TLSCertFile:                     configuration.Consensus.TLSCertFile,
		TLSKeyFile:                      configuration.Consensus.TLSKeyFile,
		TLSCAFile:                       configuration.Consensus.TLSCAFile,
	}, nil
}

// ValidateConfiguration performs external control-plane checks used by
// installation preflight without opening metadata or binding listeners.
func ValidateConfiguration(configuration config.File) error {
	if !configuration.Consensus.Enabled {
		return nil
	}
	consensusConfig, err := consensusConfiguration(configuration)
	if err != nil {
		return err
	}
	if err := consensus.ValidateConfiguration(consensusConfig); err != nil {
		return fmt.Errorf("validate controller consensus: %w", err)
	}
	return nil
}

func newMutationRPCClient(configuration config.File) (*http.Client, error) {
	baseTransport, standardTransport := http.DefaultTransport.(*http.Transport)
	if !standardTransport {
		if configuration.TLSCAFile != "" {
			return nil, fmt.Errorf("control-plane TLS CA requires a configurable HTTP transport")
		}
		return &http.Client{Transport: http.DefaultTransport}, nil
	}
	transport := baseTransport.Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute
	if configuration.TLSCAFile != "" {
		contents, err := os.ReadFile(configuration.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read control-plane TLS CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, fmt.Errorf("control-plane TLS CA contains no valid certificates")
		}
		tlsConfiguration := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		if transport.TLSClientConfig != nil {
			tlsConfiguration = transport.TLSClientConfig.Clone()
			tlsConfiguration.MinVersion = tls.VersionTLS12
			tlsConfiguration.RootCAs = roots
		}
		transport.TLSClientConfig = tlsConfiguration
	}
	return &http.Client{Transport: transport}, nil
}

func runAuthenticationBootstrap(
	ctx context.Context,
	repository *store.Repository,
	service *platformauth.Service,
	authority coordination.MutationAuthority,
	bootstrapPassword func() (string, error),
	onError func(error),
) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastReportedAt time.Time
	lastReportedError := ""
	for {
		if administrator, found := repository.PlatformUserByUsername(platformauth.DefaultAdminUsername); found {
			// A leader can fail after creating its local bootstrap artifact but before
			// committing the user. Once the first password change completes, every
			// controller removes any such root-only stale artifact on its next pass.
			if !administrator.MustChangePassword {
				_ = platformauth.RemoveBootstrapPassword(platformauth.DefaultBootstrapPasswordFile)
			}
			return
		}
		if authority == nil || authority.RequireMutationAuthority(ctx) == nil {
			password, passwordErr := bootstrapPassword()
			if passwordErr != nil {
				if onError != nil && ctx.Err() == nil {
					onError(fmt.Errorf("prepare bootstrap administrator credential: %w", passwordErr))
				}
			} else if _, err := service.EnsureBootstrapAdmin(ctx, password); err == nil {
				return
			} else if onError != nil && ctx.Err() == nil {
				now := time.Now().UTC()
				message := err.Error()
				if message != lastReportedError || lastReportedAt.IsZero() || now.Sub(lastReportedAt) >= 30*time.Second {
					onError(fmt.Errorf("bootstrap platform administrator: %w", err))
					lastReportedAt = now
					lastReportedError = message
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func bootstrapAdministratorPassword(configuration config.File) (string, error) {
	if password := strings.TrimSpace(configuration.BootstrapAdminPassword); password != "" {
		return password, nil
	}
	return platformauth.DefaultBootstrapPassword, nil
}

func authenticationRecoveryApplied(repository *store.Repository, recoveryID model.ResourceID) bool {
	for _, event := range repository.SecurityEvents() {
		if event.ResourceID == recoveryID && event.Kind == "password_recovered" {
			return true
		}
	}
	return false
}

func runAuthenticationRecovery(
	ctx context.Context,
	repository *store.Repository,
	service *platformauth.Service,
	authority coordination.MutationAuthority,
	path string,
	now func() time.Time,
	interval time.Duration,
) error {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	artifact, err := platformauth.ReadAdminRecoveryArtifact(path, now().UTC())
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if authenticationRecoveryApplied(repository, artifact.RecoveryID) {
			return os.Remove(path)
		}
		if _, found := repository.PlatformUserByUsername(platformauth.DefaultAdminUsername); found {
			if authority == nil || authority.RequireMutationAuthority(ctx) == nil {
				_, _, applyErr := service.ApplyAdminRecoveryArtifact(ctx, artifact)
				if applyErr == nil || authenticationRecoveryApplied(repository, artifact.RecoveryID) {
					return os.Remove(path)
				}
				if errors.Is(applyErr, store.ErrRecoveryRejected) {
					return applyErr
				}
				if !errors.Is(applyErr, store.ErrConflict) {
					return applyErr
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type mysqlFailoverRuntime struct {
	failureObserver discovery.PrimaryFailureObserver
	failureEvidence *coordination.FailureWindow
	safety          mysql.FailoverSafetyProvider
}

type runtimeLocks struct {
	publication *workflow.MemoryLocks
	operations  workflow.ClusterLockManager
	lifecycle   workflow.ClusterLockManager
}

type runtimeApprovalGates struct {
	*approval.Service
	administrativeToken string
}

func (gates runtimeApprovalGates) Validate(ctx context.Context, _ model.Operation, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if gates.administrativeToken == "" || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(gates.administrativeToken)) != 1 {
		return fmt.Errorf("valid administrative credential is required")
	}
	return nil
}

func newRuntimeLocks(repository *store.Repository, authority coordination.MutationAuthority) runtimeLocks {
	local := workflow.NewMemoryLocks()
	result := runtimeLocks{publication: local, operations: local, lifecycle: local}
	if repository == nil || authority == nil {
		return result
	}
	quorum := coordination.NewOperationLocks(repository, authority, 15*time.Minute, nil)
	result.operations = workflow.NewCompositeLocks(local, quorum)
	result.lifecycle = workflow.NewCompositeLocks(workflow.NewMemoryLocks(), quorum)
	return result
}

func newRuntimeSafetyGuard(authority coordination.MutationAuthority, updateMaintenance workflow.MaintenanceChecker) workflow.SafetyGuard {
	var authorityGuard workflow.SafetyGuard = workflow.AllowAllSafety{}
	if authority != nil {
		authorityGuard = workflow.AuthoritySafetyGuard{Authority: authority}
	}
	if updateMaintenance == nil {
		return authorityGuard
	}
	return workflow.NewCompositeSafetyGuard(
		workflow.MaintenanceSafetyGuard{Gate: updateMaintenance},
		authorityGuard,
	)
}

const (
	automaticFailoverFailureChecks   = 3
	automaticFailoverFailureDuration = 3 * time.Second
)

func automaticFailoverMaximumObservationGap(configuration config.File) time.Duration {
	maximumGap := 2 * automaticFailoverFailureDuration / automaticFailoverFailureChecks
	consider := func(enabled bool, intervalSeconds, timeoutSeconds int) {
		if !enabled {
			return
		}
		cadence := time.Duration(intervalSeconds) * time.Second
		if timeout := time.Duration(timeoutSeconds) * time.Second; timeout > cadence {
			cadence = timeout
		}
		// Permit one delayed scheduler round while still expiring stale failure
		// evidence before it can authorize a later, unrelated incident.
		if candidate := 2 * cadence; candidate > maximumGap {
			maximumGap = candidate
		}
	}
	consider(configuration.MySQL.AutomaticFailoverEnabled, configuration.MySQL.DiscoveryIntervalSeconds, configuration.MySQL.DiscoveryTimeoutSeconds)
	consider(configuration.PostgreSQL.AutomaticFailoverEnabled, configuration.PostgreSQL.DiscoveryIntervalSeconds, configuration.PostgreSQL.DiscoveryTimeoutSeconds)
	return maximumGap
}

func newMySQLFailoverRuntime(
	authority coordination.MutationAuthority,
	inventory coordination.FailoverInventory,
	leases writerendpoint.LeaseStore,
	transport writerendpoint.AgentTransport,
	secret string,
	now func() time.Time,
	agentQuorumGrace time.Duration,
	failureMaximumObservationGap time.Duration,
	authorizations *coordination.AgentAuthorizationTracker,
	externalFencers ...coordination.ExternalFencer,
) mysqlFailoverRuntime {
	components := mysqlFailoverRuntime{safety: mysql.UnsupportedFailoverSafetyProvider{}}
	var externalFencer coordination.ExternalFencer
	if len(externalFencers) > 0 {
		externalFencer = externalFencers[0]
	}
	if authority == nil || inventory == nil || leases == nil || ((transport == nil || secret == "") && externalFencer == nil) {
		return components
	}
	// Four consecutive observations (the initial failure plus three follow-ups)
	// across three seconds reject a single missed probe while leaving the full
	// 15-second Agent authorization expiry window and promotion inside a 30s RTO.
	failureOptions := make([]coordination.FailureWindowOption, 0, 1)
	if failureMaximumObservationGap > 0 {
		failureOptions = append(failureOptions, coordination.WithMaximumObservationGap(failureMaximumObservationGap))
	}
	failures := coordination.NewFailureWindow(automaticFailoverFailureChecks, automaticFailoverFailureDuration, failureOptions...)
	components.failureObserver = failures
	components.failureEvidence = failures
	options := make([]coordination.GuardedFailoverOption, 0, 2)
	if agentQuorumGrace > 0 {
		options = append(options, coordination.WithAgentQuorumFencing(agentQuorumGrace))
	}
	if authorizations != nil {
		options = append(options, coordination.WithAgentAuthorizationTracker(authorizations))
	}
	if externalFencer != nil {
		options = append(options, coordination.WithExternalFencer(externalFencer))
	}
	components.safety = coordination.NewGuardedFailoverSafety(failures, authority, inventory, leases, transport, secret, now, options...)
	return components
}

func newPostgreSQLRuntimeAdapter(
	configuration config.File,
	endpointProvider adapter.HAEndpointProvider,
	transport writerendpoint.AgentTransport,
	failoverSafety postgresql.FailoverSafetyProvider,
) *postgresql.Adapter {
	var nodeController postgresql.NodeController = postgresql.UnsupportedNodeController{}
	if _, err := postgresqlOperationCredentials(configuration.PostgreSQL); err == nil && transport != nil && strings.TrimSpace(configuration.Agent.SharedSecret) != "" {
		nodeController = writerendpoint.NewPostgreSQLNodeController(transport, configuration.Agent.SharedSecret, nil)
	}
	if failoverSafety == nil {
		failoverSafety = postgresql.UnsupportedFailoverSafetyProvider{}
	}
	return postgresql.NewWithProviders(
		postgresql.CLIQueryRunner{}, endpointProvider, nodeController, failoverSafety,
	)
}

func newOracleRuntimeAdapter(
	configuration config.File,
	transport writerendpoint.AgentTransport,
	endpointProviders ...adapter.HAEndpointProvider,
) *oracle.Adapter {
	var brokerController oracle.BrokerController = oracle.UnsupportedBrokerController{}
	var discoveryProvider oracle.BrokerDiscoveryProvider = oracle.UnsupportedBrokerDiscoveryProvider{}
	var endpointProvider adapter.HAEndpointProvider = oracle.UnsupportedHAEndpointProvider{}
	if len(endpointProviders) > 0 && endpointProviders[0] != nil {
		endpointProvider = endpointProviders[0]
	}
	if configuration.Oracle.Enabled && transport != nil && strings.TrimSpace(configuration.Agent.SharedSecret) != "" {
		discoveryProvider = writerendpoint.NewOracleBrokerDiscoveryProvider(
			transport, configuration.Agent.SharedSecret, nil,
		)
	}
	if _, err := oracleOperationCredentials(configuration.Oracle); err == nil &&
		transport != nil && strings.TrimSpace(configuration.Agent.SharedSecret) != "" {
		brokerController = writerendpoint.NewOracleBrokerController(
			transport, configuration.Agent.SharedSecret, nil,
		)
	}
	return oracle.NewWithRuntimeProviders(
		oracle.CLIBrokerRunner{}, oracle.CLISQLPlusRunner{}, discoveryProvider, brokerController, endpointProvider,
	)
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

func (runtime *Runtime) startAutomaticRecovery(controller *recovery.Controller) {
	if runtime == nil || controller == nil {
		return
	}
	if runtime.automaticRecovery == nil {
		runtime.automaticRecovery = controller
	}
	runtime.automaticRecoveries = append(runtime.automaticRecoveries, controller)
	runtime.startLoop(controller.Run)
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
	startedAt := time.Now().UTC()
	repository, err := store.Open(configuration.MetadataPath)
	if err != nil {
		return nil, err
	}
	result := &Runtime{}
	if configuration.Consensus.Enabled {
		consensusConfig, configErr := consensusConfiguration(configuration)
		if configErr != nil {
			return nil, configErr
		}
		result.consensus, err = consensus.Open(consensusConfig, repository)
		if err != nil {
			return nil, fmt.Errorf("start controller consensus: %w", err)
		}
		if err := repository.SetSnapshotConsensus(result.consensus); err != nil {
			_ = result.Close()
			return nil, fmt.Errorf("attach replicated metadata store: %w", err)
		}
	}
	result.authentication = platformauth.New(
		repository,
		platformauth.DefaultArgon2Hasher(rand.Reader),
		rand.Reader,
		time.Now,
		8*time.Hour,
	)
	if result.consensus == nil {
		password, passwordErr := bootstrapAdministratorPassword(configuration)
		if passwordErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("prepare bootstrap platform administrator credential: %w", passwordErr)
		}
		if _, err := result.authentication.EnsureBootstrapAdmin(context.Background(), password); err != nil {
			return nil, fmt.Errorf("bootstrap platform administrator: %w", err)
		}
	} else {
		result.startLoop(func(ctx context.Context) {
			runAuthenticationBootstrap(ctx, repository, result.authentication, result.consensus, func() (string, error) {
				return bootstrapAdministratorPassword(configuration)
			}, func(err error) {
				log.Printf("administrator bootstrap failed: %v", err)
			})
		})
	}
	if _, statErr := os.Stat(platformauth.DefaultAdminRecoveryFile); statErr == nil {
		result.startLoop(func(ctx context.Context) {
			if recoveryErr := runAuthenticationRecovery(
				ctx, repository, result.authentication, result.consensus,
				platformauth.DefaultAdminRecoveryFile, time.Now, 500*time.Millisecond,
			); recoveryErr != nil && ctx.Err() == nil {
				log.Printf("administrator recovery failed: %v", recoveryErr)
			}
		})
	} else if !os.IsNotExist(statErr) {
		_ = result.Close()
		return nil, fmt.Errorf("inspect administrator recovery artifact: %w", statErr)
	}
	registry := adapter.NewRegistry()
	var endpointProvider adapter.HAEndpointProvider = mysql.UnsupportedHAEndpointProvider{}
	var vipProvider *writerendpoint.LinuxVIPProvider
	var endpointRouter *writerendpoint.ProviderRouter
	endpointProviders := make(map[model.EndpointProviderKind]adapter.HAEndpointProvider)
	var ownershipLeases *coordination.LeaseStore
	var agentTransport writerendpoint.AgentTransport
	var kubernetesFactory kubernetes.Factory
	if result.consensus != nil {
		ownershipLeases = coordination.NewLeaseStore(repository, result.consensus, nil)
	}
	if configuration.Agent.Enabled {
		if result.consensus == nil {
			return nil, fmt.Errorf("agent-backed VIP execution requires controller consensus")
		}
		transport, transportErr := writerendpoint.NewSSHAgentTransport(writerendpoint.SSHAgentTransportConfig{
			SSHBinary: configuration.Agent.SSHBinary, User: configuration.Agent.User,
			IdentityFile: configuration.Agent.IdentityFile, KnownHostsFile: configuration.Agent.KnownHostsFile,
			AgentBinary: configuration.Agent.AgentBinary, AgentConfigPath: configuration.Agent.AgentConfigPath,
			CommandTimeout:        time.Duration(configuration.Agent.CommandTimeoutSeconds) * time.Second,
			MutationTimeout:       time.Duration(configuration.Agent.MutationTimeoutSeconds) * time.Second,
			MaxConcurrentSessions: configuration.Agent.MaxConcurrentSessions,
		}, writerendpoint.OSProcessRunner{})
		if transportErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure agent transport: %w", transportErr)
		}
		agentTransport = transport
		vipProvider = writerendpoint.NewLinuxVIPProvider(repository, transport, ownershipLeases, configuration.Agent.SharedSecret, nil)
		endpointProviders[model.EndpointProviderLinuxVIP] = vipProvider
	}
	if configuration.Kubernetes.Enabled {
		if result.consensus == nil || ownershipLeases == nil {
			return nil, fmt.Errorf("Kubernetes Service endpoint execution requires controller consensus")
		}
		factory := kubernetes.ClientFactory{Timeout: time.Duration(configuration.Kubernetes.RequestTimeoutSeconds) * time.Second}
		kubernetesFactory = factory
		endpointProviders[model.EndpointProviderKubernetesService] = writerendpoint.NewKubernetesServiceProvider(repository, factory, ownershipLeases)
	}
	if len(endpointProviders) > 0 {
		endpointRouter = writerendpoint.NewProviderRouter(repository, endpointProviders)
		endpointProvider = endpointRouter
	}
	var failoverAuthority coordination.MutationAuthority
	var failoverLeases writerendpoint.LeaseStore
	if result.consensus != nil {
		failoverAuthority = result.consensus
	}
	if ownershipLeases != nil {
		failoverLeases = ownershipLeases
	}
	var agentAuthorizations *coordination.AgentAuthorizationTracker
	if result.consensus != nil && configuration.Agent.Enabled {
		agentAuthorizations = coordination.NewAgentAuthorizationTracker()
	}
	var commandFencer coordination.ExternalFencer
	if configuration.Fencing.Enabled {
		configuredFencer, fencerErr := coordination.NewCommandFencer(
			configuration.Fencing.ExecutablePath,
			time.Duration(configuration.Fencing.TimeoutSeconds)*time.Second,
			nil,
		)
		if fencerErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure external fencing provider: %w", fencerErr)
		}
		commandFencer = configuredFencer
	}
	var kubernetesFencer coordination.ExternalFencer
	if configuration.Kubernetes.Enabled {
		kubernetesFencer = newKubernetesWorkloadFencer(repository, kubernetesFactory, time.Duration(configuration.Kubernetes.FenceTimeoutSeconds)*time.Second)
	}
	var externalFencer coordination.ExternalFencer
	if commandFencer != nil || kubernetesFencer != nil {
		externalFencer = runtimeFencerRouter{inventory: repository, kubernetes: kubernetesFencer, fallback: commandFencer}
	}
	agentQuorumGrace := time.Duration(0)
	if configuration.Fencing.AgentQuorumEnabled {
		agentQuorumGrace = time.Duration(configuration.Fencing.AgentQuorumGraceSeconds) * time.Second
	}
	failoverRuntime := newMySQLFailoverRuntime(
		failoverAuthority, repository, failoverLeases, agentTransport, configuration.Agent.SharedSecret, nil,
		agentQuorumGrace,
		automaticFailoverMaximumObservationGap(configuration), agentAuthorizations, externalFencer,
	)
	mysqlAdapter := mysql.NewWithSafetyProviders(
		mysql.CLIQueryRunner{},
		endpointProvider,
		repository,
		failoverRuntime.safety,
	).RequireSemiSync(configuration.MySQL.SemiSyncRequired)
	postgresqlAdapter := newPostgreSQLRuntimeAdapter(configuration, endpointProvider, agentTransport, failoverRuntime.safety)
	oracleAdapter := newOracleRuntimeAdapter(configuration, agentTransport, endpointProvider)
	powerShutdownAdapter := workflow.NewPowerShutdownAdapter(repository, agentTransport, configuration.Agent.SharedSecret)
	for _, candidate := range []adapter.DatabaseHAAdapter{
		mysqlAdapter,
		postgresqlAdapter,
		oracleAdapter,
		sqlserver.New(),
		powerShutdownAdapter,
	} {
		if err := registry.Register(candidate); err != nil {
			_ = result.Close()
			return nil, fmt.Errorf("register %s adapter: %w", candidate.Engine(), err)
		}
	}
	locks := newRuntimeLocks(repository, failoverAuthority)
	updateMaintenance := maintenance.NewGate(maintenance.DefaultMarkerPath, repository)
	softwareUpdates := platformupdate.NewManager(platformupdate.Config{})
	operationCredentials := func(_ context.Context, cluster model.DatabaseCluster) (adapter.OperationCredentials, error) {
		return databaseOperationCredentials(configuration, cluster)
	}
	approvalService := approval.New(repository, rand.Reader, time.Now)
	approvalGates := runtimeApprovalGates{Service: approvalService, administrativeToken: configuration.ControlToken}
	service := workflow.New(
		registry,
		workflow.TopologyDiscovery{Reader: repository},
		newRuntimeSafetyGuard(failoverAuthority, updateMaintenance),
		locks.operations,
		approvalGates,
		repository,
		workflow.WithOperationStore(repository),
		workflow.WithOperationResolver(workflow.RepositoryResolver{
			Reader:      repository,
			Credentials: workflow.CredentialProviderFunc(operationCredentials),
		}),
	)
	result.startLoop(func(ctx context.Context) {
		runAbandonedOperationReconciler(ctx, repository, service, result.consensus)
	})
	discoveryOptions := []discovery.Option{discovery.WithPublicationFence(locks.publication)}
	if failoverRuntime.failureObserver != nil {
		discoveryOptions = append(discoveryOptions, discovery.WithPrimaryFailureObserver(failoverRuntime.failureObserver))
	}
	refresher := discovery.New(registry, repository, discovery.CredentialResolverFunc(func(_ context.Context, cluster model.DatabaseCluster, _ model.Endpoint) (adapter.Credentials, error) {
		return databaseDiscoveryCredentials(configuration, cluster)
	}), nil, discoveryOptions...)
	options := []api.ServerOption{
		api.WithControlToken(configuration.ControlToken),
		api.WithMonitoringToken(configuration.MonitoringToken),
		api.WithApprovalService(approvalService),
		api.WithAuthentication(result.authentication),
		api.WithSecureCookies(strings.TrimSpace(configuration.TLSCertFile) != ""),
		api.WithControlPlaneStatus(newControlPlaneStatusProvider(repository, result.consensus, startedAt)),
		api.WithMutationMaintenance(updateMaintenance),
		api.WithSoftwareUpdates(softwareUpdates),
	}
	if agentTransport != nil && result.consensus != nil && ownershipLeases != nil {
		driver := &disasterDriver{repository: repository, transport: agentTransport, secret: configuration.Agent.SharedSecret, authority: result.consensus, leases: ownershipLeases, refresher: refresher, mysql: mysql.DisasterExecutor{Runner: mysql.CLIQueryRunner{}, SemiSyncRequired: configuration.MySQL.SemiSyncRequired}, credentials: operationCredentials}
		manager := newDisasterManager(repository, result.consensus, locks, updateMaintenance, driver)
		options = append(options, api.WithDisasterRecovery(manager, result.startLoop))
	}
	if configuration.Agent.Enabled {
		options = append(options, api.WithAgentReconcileSecret(configuration.Agent.SharedSecret))
		if agentAuthorizations != nil {
			options = append(options, api.WithAgentAuthorizationTracker(agentAuthorizations))
		}
	}
	if result.consensus != nil {
		mutationRPCClient, clientErr := newMutationRPCClient(configuration)
		if clientErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure leader mutation RPC: %w", clientErr)
		}
		options = append(options,
			api.WithMutationAuthority(result.consensus),
			api.WithMutationRPC(api.NewLeaderMutationRPCClient(mutationRPCClient, repository)),
		)
	}
	if configuration.NodeLifecycle.Enabled {
		if result.consensus == nil {
			_ = result.Close()
			return nil, fmt.Errorf("node lifecycle execution requires controller consensus")
		}
		executor, executorErr := lifecycle.NewShellExecutor(configuration.NodeLifecycle.ExecutorPath, lifecycle.OSLifecycleProcessRunner{}, lifecycle.WithShellEnvironment(lifecycle.ShellEnvironment{
			PackageRepository:       configuration.NodeLifecycle.PackageRepository,
			KnownHostsFile:          configuration.NodeLifecycle.KnownHostsFile,
			IdentityFile:            configuration.NodeLifecycle.IdentityFile,
			JQBinary:                configuration.NodeLifecycle.JQBinary,
			AdapterRuntimeHelper:    configuration.NodeLifecycle.AdapterRuntimeHelper,
			ControlJoinHelper:       configuration.NodeLifecycle.ControlJoinHelper,
			ControlAPIIssuerCert:    configuration.NodeLifecycle.ControlAPIIssuerCertFile,
			ControlAPIIssuerKey:     configuration.NodeLifecycle.ControlAPIIssuerKeyFile,
			ControlRaftIssuerCert:   configuration.NodeLifecycle.ControlRaftIssuerCertFile,
			ControlRaftIssuerKey:    configuration.NodeLifecycle.ControlRaftIssuerKeyFile,
			ControlCertValidityDays: configuration.NodeLifecycle.ControlCertificateValidityDays,
			CloneHelper:             configuration.NodeLifecycle.CloneHelper,
			XtraBackupHelper:        configuration.NodeLifecycle.XtraBackupHelper,
			PostgreSQLInstallHelper: configuration.NodeLifecycle.PostgreSQLInstallHelper,
			PostgreSQLSyncHelper:    configuration.NodeLifecycle.PostgreSQLSyncHelper,
			MySQLRootRemoteHost:     configuration.NodeLifecycle.MySQLRootRemoteHost,
		}))
		if executorErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure node lifecycle executor: %w", executorErr)
		}
		controllerMembership, membershipErr := newConsensusLifecycleMembership(result.consensus, configuration)
		if membershipErr != nil {
			_ = result.Close()
			return nil, fmt.Errorf("configure controller lifecycle membership: %w", membershipErr)
		}
		manager := lifecycle.NewManager(
			repository, result.consensus, lifecycle.NewCompositeSafetyGuard(
				lifecycle.MaintenanceSafetyGuard{Gate: updateMaintenance},
				lifecycle.PlanSafetyGuard{},
			), locks.lifecycle,
			lifecycle.TokenApproval{ExpectedToken: configuration.ApprovalToken}, executor, repository, nil,
			lifecycle.WithControllerMembership(controllerMembership),
		)
		secrets := nodeLifecycleSecrets(configuration.NodeLifecycle, configuration.MySQL)
		options = append(options, api.WithNodeLifecycle(manager, nodeLifecycleCapabilities(configuration.NodeLifecycle), api.LifecycleSecretProviderFunc(func(context.Context, lifecycle.Request) (lifecycle.ExecutionSecrets, error) {
			return secrets, nil
		})))
	}
	result.server = api.NewServer(registry, repository, service, refresher, options...)
	var discoveryAuthority discovery.ScheduledMutationAuthority
	if result.consensus != nil {
		discoveryAuthority = result.consensus
	}
	for _, schedule := range configuredDiscoverySchedules(configuration) {
		scheduler := discovery.NewScheduler(
			engineSetClusterSource{source: repository, engines: schedule.engines},
			refresher,
			discoveryAuthority,
			schedule.interval,
			schedule.timeout,
		)
		result.startLoop(scheduler.Run)
	}
	if configuration.MySQL.AutomaticFailoverEnabled {
		if result.consensus == nil || failoverRuntime.failureEvidence == nil || (!configuration.Agent.Enabled && !configuration.Kubernetes.Enabled) {
			_ = result.Close()
			return nil, fmt.Errorf("automatic MySQL failover requires consensus, runtime fencing, and failure evidence")
		}
		controller := recovery.NewController(
			repository, failoverRuntime.failureEvidence, recovery.NewMySQLCandidateSelector(mysqlAdapter), service,
			result.consensus,
			time.Duration(configuration.MySQL.AutomaticFailoverRetrySeconds)*time.Second, nil,
			recovery.WithInterval(time.Duration(configuration.MySQL.AutomaticFailoverIntervalSeconds)*time.Second),
		)
		result.startAutomaticRecovery(controller)
	}
	if configuration.PostgreSQL.AutomaticFailoverEnabled {
		if result.consensus == nil || failoverRuntime.failureEvidence == nil || !configuration.Agent.Enabled {
			_ = result.Close()
			return nil, fmt.Errorf("automatic PostgreSQL failover requires consensus, agent fencing, and failure evidence")
		}
		controller := recovery.NewController(
			repository, failoverRuntime.failureEvidence, recovery.NewPostgreSQLCandidateSelector(postgresqlAdapter), service,
			result.consensus,
			time.Duration(configuration.PostgreSQL.AutomaticFailoverRetrySeconds)*time.Second, nil,
			recovery.WithEngine(model.EnginePostgreSQL),
			recovery.WithInterval(time.Duration(configuration.PostgreSQL.AutomaticFailoverIntervalSeconds)*time.Second),
		)
		result.startAutomaticRecovery(controller)
	}
	if endpointRouter != nil && ownershipLeases != nil && result.consensus != nil {
		keeper := coordination.NewOwnershipKeeper(repository, endpointRouter, ownershipLeases, result.consensus, nil, 5*time.Second, 15*time.Second)
		result.startLoop(keeper.Run)
	}
	return result, nil
}

func nodeLifecycleSecrets(configuration config.NodeLifecycle, mysqlConfiguration config.MySQL) lifecycle.ExecutionSecrets {
	replicationPassword := configuration.ReplicationPassword
	if strings.TrimSpace(mysqlConfiguration.Replication.Password) != "" {
		replicationPassword = mysqlConfiguration.Replication.Password
	}
	return lifecycle.ExecutionSecrets{
		SSHPassword: configuration.SSHPassword, MySQLRootPassword: configuration.MySQLRootPassword,
		MySQLDiscoveryUsername:        mysqlConfiguration.Discovery.Username,
		MySQLDiscoveryPassword:        mysqlConfiguration.Discovery.Password,
		MySQLOperationUsername:        mysqlConfiguration.Operation.Username,
		MySQLOperationPassword:        mysqlConfiguration.Operation.Password,
		MySQLReplicationUsername:      mysqlConfiguration.Replication.Username,
		ReplicationPassword:           replicationPassword,
		PostgreSQLAdminPassword:       configuration.PostgreSQLAdminPassword,
		PostgreSQLReplicationPassword: configuration.PostgreSQLReplicationPassword,
	}
}

func nodeLifecycleCapabilities(configuration config.NodeLifecycle) lifecycle.Capabilities {
	versions := make(map[string]bool, len(configuration.XtraBackupVersions))
	for version, available := range configuration.XtraBackupVersions {
		versions[version] = available
	}
	return lifecycle.Capabilities{
		CloneAvailable: configuration.CloneAvailable, XtraBackupVersions: versions, LogicalDumpAllowed: configuration.LogicalDumpAllowed,
		PostgreSQLBaseBackupAvailable: configuration.PostgreSQLBaseBackupAvailable,
		PostgreSQLRewindAvailable:     configuration.PostgreSQLRewindAvailable,
	}
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

func postgresqlDiscoveryCredentials(configuration config.PostgreSQL) (adapter.Credentials, error) {
	if !configuration.Enabled || configuration.Discovery.Username == "" || configuration.Discovery.Password == "" {
		return adapter.Credentials{}, fmt.Errorf("PostgreSQL discovery credentials are not configured")
	}
	database := strings.TrimSpace(configuration.Discovery.Database)
	if database == "" {
		database = "postgres"
	}
	return adapter.Credentials{
		Username: configuration.Discovery.Username,
		Password: configuration.Discovery.Password,
		Database: database,
	}, nil
}

func postgresqlOperationCredentials(configuration config.PostgreSQL) (adapter.OperationCredentials, error) {
	if !configuration.Enabled {
		return adapter.OperationCredentials{}, fmt.Errorf("PostgreSQL operation credentials are not configured")
	}
	if configuration.Operation.Username == "" || configuration.Operation.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("PostgreSQL operation credentials are incomplete")
	}
	if configuration.Replication.Username == "" || configuration.Replication.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("PostgreSQL replication credentials are incomplete")
	}
	operationDatabase := strings.TrimSpace(configuration.Operation.Database)
	if operationDatabase == "" {
		operationDatabase = "postgres"
	}
	replicationDatabase := strings.TrimSpace(configuration.Replication.Database)
	if replicationDatabase == "" {
		replicationDatabase = operationDatabase
	}
	return adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: configuration.Operation.Username, Password: configuration.Operation.Password, Database: operationDatabase},
		Replication:    adapter.Credentials{Username: configuration.Replication.Username, Password: configuration.Replication.Password, Database: replicationDatabase},
	}, nil
}

func oracleDiscoveryCredentials(configuration config.Oracle) (adapter.Credentials, error) {
	if !configuration.Enabled || configuration.Discovery.Username == "" || configuration.Discovery.Password == "" {
		return adapter.Credentials{}, fmt.Errorf("Oracle discovery credentials are not configured")
	}
	return adapter.Credentials{
		Username: configuration.Discovery.Username,
		Password: configuration.Discovery.Password,
		Database: strings.TrimSpace(configuration.Discovery.Database),
	}, nil
}

func oracleOperationCredentials(configuration config.Oracle) (adapter.OperationCredentials, error) {
	if !configuration.Enabled {
		return adapter.OperationCredentials{}, fmt.Errorf("Oracle operation credentials are not configured")
	}
	if configuration.Operation.Username == "" || configuration.Operation.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("Oracle operation credentials are incomplete")
	}
	database := strings.TrimSpace(configuration.Operation.Database)
	if database == "" {
		database = strings.TrimSpace(configuration.Discovery.Database)
	}
	return adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: configuration.Operation.Username, Password: configuration.Operation.Password, Database: database},
	}, nil
}

func sqlServerDiscoveryCredentials(configuration config.SQLServer) (adapter.Credentials, error) {
	if !configuration.Enabled || configuration.Discovery.Username == "" || configuration.Discovery.Password == "" {
		return adapter.Credentials{}, fmt.Errorf("SQL Server discovery credentials are not configured")
	}
	database := strings.TrimSpace(configuration.Discovery.Database)
	if database == "" {
		database = "master"
	}
	return adapter.Credentials{Username: configuration.Discovery.Username, Password: configuration.Discovery.Password, Database: database}, nil
}

func sqlServerOperationCredentials(configuration config.SQLServer) (adapter.OperationCredentials, error) {
	if !configuration.Enabled {
		return adapter.OperationCredentials{}, fmt.Errorf("SQL Server operation credentials are not configured")
	}
	if configuration.Operation.Username == "" || configuration.Operation.Password == "" {
		return adapter.OperationCredentials{}, fmt.Errorf("SQL Server operation credentials are incomplete")
	}
	database := strings.TrimSpace(configuration.Operation.Database)
	if database == "" {
		database = strings.TrimSpace(configuration.Discovery.Database)
	}
	if database == "" {
		database = "master"
	}
	return adapter.OperationCredentials{
		Administrative: adapter.Credentials{Username: configuration.Operation.Username, Password: configuration.Operation.Password, Database: database},
	}, nil
}

func databaseDiscoveryCredentials(configuration config.File, cluster model.DatabaseCluster) (adapter.Credentials, error) {
	switch cluster.Engine {
	case model.EngineMySQL:
		return mysqlDiscoveryCredentials(configuration.MySQL)
	case model.EnginePostgreSQL:
		return postgresqlDiscoveryCredentials(configuration.PostgreSQL)
	case model.EngineOracle:
		return oracleDiscoveryCredentials(configuration.Oracle)
	case model.EngineSQLServer:
		return sqlServerDiscoveryCredentials(configuration.SQLServer)
	default:
		return adapter.Credentials{}, adapter.ErrUnsupported
	}
}

func databaseOperationCredentials(configuration config.File, cluster model.DatabaseCluster) (adapter.OperationCredentials, error) {
	switch cluster.Engine {
	case model.EngineMySQL:
		return mysqlOperationCredentials(configuration.MySQL)
	case model.EnginePostgreSQL:
		return postgresqlOperationCredentials(configuration.PostgreSQL)
	case model.EngineOracle:
		return oracleOperationCredentials(configuration.Oracle)
	case model.EngineSQLServer:
		return sqlServerOperationCredentials(configuration.SQLServer)
	default:
		return adapter.OperationCredentials{}, adapter.ErrUnsupported
	}
}
