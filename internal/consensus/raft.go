package consensus

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/controlstate"
	"clusterguard.io/ha/pkg/model"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

var (
	ErrNotLeader           = errors.New("local controller is not the Raft leader")
	ErrNoQuorum            = errors.New("Raft leader cannot confirm controller quorum")
	ErrCommitIndeterminate = errors.New("Raft commit outcome is indeterminate")
)

const maximumReplicatedStateBytes = controlstate.MaximumBytes

const (
	raftSnapshotThreshold = 64
	raftTrailingLogs      = 32
	raftSnapshotInterval  = 30 * time.Second
)

type Peer struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
	APIAddress string           `json:"api_address,omitempty"`
}

type Config struct {
	LocalID                model.ResourceID
	BindAddress            string
	AdvertiseAddress       string
	DataDirectory          string
	Peers                  []Peer
	Bootstrap              bool
	ApplyTimeout           time.Duration
	SnapshotCASEnabled     bool
	AllowInsecureTransport bool
	TLSCertFile            string
	TLSKeyFile             string
	TLSCAFile              string
}

type StateMachine interface {
	ValidateReplicatedState([]byte) error
	ApplyReplicatedState([]byte) error
}

type snapshotStateRestorer interface {
	RestoreReplicatedState([]byte) error
}

type applyValidatingStateMachine interface {
	ApplyValidatesReplicatedState() bool
}

func validatesReplicatedStateDuringApply(machine StateMachine) bool {
	validator, ok := machine.(applyValidatingStateMachine)
	return ok && validator.ApplyValidatesReplicatedState()
}

type committedStateError interface {
	Committed() bool
}

func stateWasCommitted(err error) bool {
	if err == nil {
		return false
	}
	committed := committedStateError(nil)
	return errors.As(err, &committed) && committed.Committed()
}

func validateConfig(configuration Config) error {
	if !model.ValidResourceID(configuration.LocalID) || strings.TrimSpace(configuration.DataDirectory) == "" {
		return fmt.Errorf("valid local controller UUID and data directory are required")
	}
	if len(configuration.Peers) < 3 || len(configuration.Peers)%2 == 0 {
		return fmt.Errorf("Raft controller membership must be an odd set of at least three voters")
	}
	ids := make(map[model.ResourceID]struct{}, len(configuration.Peers))
	addresses := make(map[string]struct{}, len(configuration.Peers))
	localFound := false
	for _, peer := range configuration.Peers {
		address := strings.TrimSpace(peer.Address)
		if !model.ValidResourceID(peer.ResourceID) {
			return fmt.Errorf("Raft peer UUID is invalid")
		}
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("Raft peer address is invalid")
		}
		if _, duplicate := ids[peer.ResourceID]; duplicate {
			return fmt.Errorf("Raft membership contains a duplicate controller UUID")
		}
		if _, duplicate := addresses[address]; duplicate {
			return fmt.Errorf("Raft membership contains a duplicate address")
		}
		ids[peer.ResourceID] = struct{}{}
		addresses[address] = struct{}{}
		if peer.ResourceID == configuration.LocalID {
			localFound = true
		}
	}
	if !localFound {
		return fmt.Errorf("local controller is outside Raft membership")
	}
	for _, address := range []string{configuration.BindAddress, configuration.AdvertiseAddress} {
		if _, _, err := net.SplitHostPort(strings.TrimSpace(address)); err != nil {
			return fmt.Errorf("Raft bind or advertise address is invalid")
		}
	}
	tlsPaths := []string{
		strings.TrimSpace(configuration.TLSCertFile),
		strings.TrimSpace(configuration.TLSKeyFile),
		strings.TrimSpace(configuration.TLSCAFile),
	}
	configuredTLSPaths := 0
	for _, path := range tlsPaths {
		if path != "" {
			configuredTLSPaths++
			if !filepath.IsAbs(path) {
				return fmt.Errorf("Raft TLS certificate, key, and CA paths must be absolute")
			}
		}
	}
	if configuredTLSPaths != 0 && configuredTLSPaths != len(tlsPaths) {
		return fmt.Errorf("Raft TLS certificate, key, and CA must be configured together")
	}
	if configuredTLSPaths == 0 && raftUsesExternalNetwork(configuration) && !configuration.AllowInsecureTransport {
		return fmt.Errorf("Raft TLS is required for transport on a non-loopback network")
	}
	return nil
}

// ValidateConfiguration verifies Raft membership and the local transport
// identity without binding the listener or opening persistent state.
func ValidateConfiguration(configuration Config) error {
	if err := validateConfig(configuration); err != nil {
		return err
	}
	if strings.TrimSpace(configuration.TLSCertFile) == "" {
		return nil
	}
	return validateRaftTLSIdentity(
		configuration.TLSCertFile,
		configuration.TLSKeyFile,
		configuration.TLSCAFile,
		configuration.AdvertiseAddress,
	)
}

func raftAddressIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func raftUsesExternalNetwork(configuration Config) bool {
	for _, address := range []string{configuration.BindAddress, configuration.AdvertiseAddress} {
		if !raftAddressIsLoopback(address) {
			return true
		}
	}
	for _, peer := range configuration.Peers {
		if !raftAddressIsLoopback(peer.Address) {
			return true
		}
	}
	return false
}

type raftTLSStreamLayer struct {
	listener  net.Listener
	advertise net.Addr
	serverTLS *tls.Config
	clientTLS *tls.Config
}

func validateRaftTLSIdentity(certFile, keyFile, caFile, advertiseAddress string) error {
	identity, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load Raft TLS identity: %w", err)
	}
	if len(identity.Certificate) == 0 {
		return fmt.Errorf("Raft TLS identity contains no certificate")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse Raft TLS identity: %w", err)
	}
	caContents, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("read Raft TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caContents) {
		return fmt.Errorf("Raft TLS CA contains no valid certificates")
	}
	intermediates := x509.NewCertPool()
	for _, encoded := range identity.Certificate[1:] {
		certificate, parseErr := x509.ParseCertificate(encoded)
		if parseErr != nil {
			return fmt.Errorf("parse Raft TLS certificate chain: %w", parseErr)
		}
		intermediates.AddCert(certificate)
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(advertiseAddress))
	if err != nil {
		return fmt.Errorf("parse Raft advertised address for TLS validation: %w", err)
	}
	host = strings.Trim(host, "[]")
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, DNSName: host,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("Raft TLS identity cannot authenticate advertised address %q for server authentication: %w", host, err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return fmt.Errorf("Raft TLS identity is not valid for client authentication: %w", err)
	}
	return nil
}

func newRaftTLSStreamLayer(bindAddress string, advertise net.Addr, certFile, keyFile, caFile string) (*raftTLSStreamLayer, error) {
	if advertise == nil {
		return nil, fmt.Errorf("Raft TLS advertised address is required")
	}
	if err := validateRaftTLSIdentity(certFile, keyFile, caFile, advertise.String()); err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load Raft TLS identity: %w", err)
	}
	caContents, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read Raft TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caContents) {
		return nil, fmt.Errorf("Raft TLS CA contains no valid certificates")
	}
	listener, err := net.Listen("tcp", bindAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for Raft TLS: %w", err)
	}
	if advertise == nil {
		advertise = listener.Addr()
	}
	return &raftTLSStreamLayer{
		listener:  listener,
		advertise: advertise,
		serverTLS: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
			ClientCAs:    roots,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		},
		clientTLS: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
			RootCAs:      roots,
		},
	}, nil
}

func (layer *raftTLSStreamLayer) Accept() (net.Conn, error) {
	connection, err := layer.listener.Accept()
	if err != nil {
		return nil, err
	}
	return tls.Server(connection, layer.serverTLS), nil
}

func (layer *raftTLSStreamLayer) Close() error { return layer.listener.Close() }

func (layer *raftTLSStreamLayer) Addr() net.Addr { return layer.advertise }

func (layer *raftTLSStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	target := string(address)
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid Raft TLS peer address: %w", err)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rawConnection, err := (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	clientConfiguration := layer.clientTLS.Clone()
	clientConfiguration.ServerName = strings.Trim(host, "[]")
	connection := tls.Client(rawConnection, clientConfiguration)
	if err := connection.HandshakeContext(ctx); err != nil {
		_ = rawConnection.Close()
		return nil, fmt.Errorf("authenticate Raft TLS peer: %w", err)
	}
	return connection, nil
}

func encodeReplicatedLog(state []byte) ([]byte, error) {
	if len(state) == 0 || len(state) > maximumReplicatedStateBytes {
		return nil, fmt.Errorf("replicated state is invalid")
	}
	return append([]byte{}, state...), nil
}

func decodeReplicatedLog(contents []byte) ([]byte, error) {
	if len(contents) == 0 || len(contents) > maximumReplicatedStateBytes {
		return nil, fmt.Errorf("replicated state size is invalid")
	}
	return append([]byte{}, contents...), nil
}

type replicatedFSM struct {
	machine StateMachine
	mu      sync.RWMutex
	state   []byte
}

func (fsm *replicatedFSM) Apply(log *raft.Log) interface{} {
	state, err := decodeReplicatedLog(log.Data)
	if err != nil {
		return err
	}
	if !validatesReplicatedStateDuringApply(fsm.machine) {
		if err := fsm.machine.ValidateReplicatedState(state); err != nil {
			return err
		}
	}
	applyErr := fsm.machine.ApplyReplicatedState(state)
	if applyErr != nil && !stateWasCommitted(applyErr) {
		return applyErr
	}
	fsm.mu.Lock()
	fsm.state = state
	fsm.mu.Unlock()
	return applyErr
}

func (fsm *replicatedFSM) Snapshot() (raft.FSMSnapshot, error) {
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	return &replicatedSnapshot{state: append([]byte{}, fsm.state...)}, nil
}

func (fsm *replicatedFSM) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	state, err := io.ReadAll(io.LimitReader(reader, maximumReplicatedStateBytes+1))
	if err != nil {
		return fmt.Errorf("read Raft snapshot: %w", err)
	}
	if len(state) > maximumReplicatedStateBytes {
		return fmt.Errorf("Raft snapshot exceeds maximum state size")
	}
	if len(state) == 0 {
		return nil
	}
	if !validatesReplicatedStateDuringApply(fsm.machine) {
		if err := fsm.machine.ValidateReplicatedState(state); err != nil {
			return err
		}
	}
	apply := fsm.machine.ApplyReplicatedState
	if restorer, ok := fsm.machine.(snapshotStateRestorer); ok {
		apply = restorer.RestoreReplicatedState
	}
	applyErr := apply(state)
	if applyErr != nil && !stateWasCommitted(applyErr) {
		return applyErr
	}
	fsm.mu.Lock()
	fsm.state = append([]byte{}, state...)
	fsm.mu.Unlock()
	return applyErr
}

type replicatedSnapshot struct{ state []byte }

func (snapshot *replicatedSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := io.Copy(sink, bytes.NewReader(snapshot.state)); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (*replicatedSnapshot) Release() {}

type Node struct {
	raft                   *raft.Raft
	transport              *raft.NetworkTransport
	store                  *raftboltdb.BoltStore
	fsm                    *replicatedFSM
	localID                model.ResourceID
	voterCount             int
	applyTimeout           time.Duration
	snapshotCAS            bool
	leaderAPIs             map[model.ResourceID]string
	commitMu               sync.Mutex
	synchronizeMu          sync.Mutex
	synchronizedCommitTerm uint64
	closeOnce              sync.Once
	closeErr               error
}

func newRaftRuntimeConfiguration(localID model.ResourceID) *raft.Config {
	configuration := raft.DefaultConfig()
	configuration.LocalID = raft.ServerID(localID)
	configuration.SnapshotThreshold = raftSnapshotThreshold
	configuration.TrailingLogs = raftTrailingLogs
	configuration.SnapshotInterval = raftSnapshotInterval
	return configuration
}

func Open(configuration Config, machine StateMachine) (*Node, error) {
	if machine == nil {
		return nil, fmt.Errorf("Raft state machine is required")
	}
	if err := ValidateConfiguration(configuration); err != nil {
		return nil, err
	}
	if configuration.ApplyTimeout <= 0 {
		configuration.ApplyTimeout = 10 * time.Second
	}
	if err := os.MkdirAll(configuration.DataDirectory, 0700); err != nil {
		return nil, fmt.Errorf("create Raft data directory: %w", err)
	}
	if err := os.Chmod(configuration.DataDirectory, 0700); err != nil {
		return nil, fmt.Errorf("secure Raft data directory: %w", err)
	}
	boltStore, err := raftboltdb.NewBoltStore(filepath.Join(configuration.DataDirectory, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("open Raft store: %w", err)
	}
	snapshotStore, err := raft.NewFileSnapshotStore(configuration.DataDirectory, 3, io.Discard)
	if err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("open Raft snapshot store: %w", err)
	}
	advertise, err := net.ResolveTCPAddr("tcp", configuration.AdvertiseAddress)
	if err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("resolve Raft advertise address: %w", err)
	}
	var transport *raft.NetworkTransport
	if strings.TrimSpace(configuration.TLSCertFile) != "" {
		stream, streamErr := newRaftTLSStreamLayer(
			configuration.BindAddress, advertise,
			configuration.TLSCertFile, configuration.TLSKeyFile, configuration.TLSCAFile,
		)
		if streamErr != nil {
			_ = boltStore.Close()
			return nil, fmt.Errorf("open Raft TLS transport: %w", streamErr)
		}
		transport = raft.NewNetworkTransport(stream, 5, 10*time.Second, io.Discard)
	} else {
		transport, err = raft.NewTCPTransport(configuration.BindAddress, advertise, 5, 10*time.Second, io.Discard)
		if err != nil {
			_ = boltStore.Close()
			return nil, fmt.Errorf("open Raft transport: %w", err)
		}
	}
	node := &Node{
		transport: transport, store: boltStore, applyTimeout: configuration.ApplyTimeout,
		localID: configuration.LocalID, voterCount: len(configuration.Peers),
		snapshotCAS: configuration.SnapshotCASEnabled, leaderAPIs: make(map[model.ResourceID]string, len(configuration.Peers)),
	}
	for _, peer := range configuration.Peers {
		if address := strings.TrimRight(strings.TrimSpace(peer.APIAddress), "/"); address != "" {
			node.leaderAPIs[peer.ResourceID] = address
		}
	}
	node.fsm = &replicatedFSM{machine: machine}
	raftConfiguration := newRaftRuntimeConfiguration(configuration.LocalID)
	raftConfiguration.LogOutput = io.Discard
	instance, err := raft.NewRaft(raftConfiguration, node.fsm, boltStore, boltStore, snapshotStore, transport)
	if err != nil {
		_ = transport.Close()
		_ = boltStore.Close()
		return nil, fmt.Errorf("start Raft controller: %w", err)
	}
	node.raft = instance
	if configuration.Bootstrap {
		existing, stateErr := raft.HasExistingState(boltStore, boltStore, snapshotStore)
		if stateErr != nil {
			_ = node.Close()
			return nil, fmt.Errorf("inspect Raft state: %w", stateErr)
		}
		if !existing {
			servers := make([]raft.Server, 0, len(configuration.Peers))
			for _, peer := range configuration.Peers {
				servers = append(servers, raft.Server{ID: raft.ServerID(peer.ResourceID), Address: raft.ServerAddress(peer.Address), Suffrage: raft.Voter})
			}
			if err := instance.BootstrapCluster(raft.Configuration{Servers: servers}).Error(); err != nil {
				_ = node.Close()
				return nil, fmt.Errorf("bootstrap Raft controller set: %w", err)
			}
		}
	}
	return node, nil
}

// SnapshotCASActive reports whether every controller is expected to enforce
// snapshot content compare-and-swap validation for replicated mutations.
func (node *Node) SnapshotCASActive() bool {
	return node != nil && node.snapshotCAS
}

type Status struct {
	Enabled           bool             `json:"enabled"`
	LocalControllerID model.ResourceID `json:"local_controller_id,omitempty"`
	Role              string           `json:"role"`
	LeaderID          model.ResourceID `json:"leader_id,omitempty"`
	LeaderAddress     string           `json:"leader_address,omitempty"`
	LeaderAPIAddress  string           `json:"leader_api_address,omitempty"`
	LeaderKnown       bool             `json:"leader_known"`
	VoterCount        int              `json:"voter_count"`
	QuorumConfirmed   bool             `json:"quorum_confirmed"`
	MutationAuthority bool             `json:"mutation_authority"`
	SnapshotCASActive bool             `json:"snapshot_cas_active"`
	Term              uint64           `json:"term"`
	LastIndex         uint64           `json:"last_index"`
	CommitIndex       uint64           `json:"commit_index"`
	AppliedIndex      uint64           `json:"applied_index"`
}

// Status returns a point-in-time control-plane view. QuorumConfirmed is true
// only when the local leader has actively verified a quorum in the supplied
// context; followers never infer quorum merely from having seen a leader.
func (node *Node) Status(ctx context.Context) Status {
	status := Status{Role: "disabled"}
	if node == nil || node.raft == nil {
		return status
	}
	status.Enabled = true
	status.LocalControllerID = node.localID
	status.Role = strings.ToLower(node.raft.State().String())
	status.VoterCount = node.voterCount
	status.SnapshotCASActive = node.snapshotCAS
	status.Term = node.raft.CurrentTerm()
	status.LastIndex = node.raft.LastIndex()
	status.CommitIndex, _ = strconv.ParseUint(node.raft.Stats()["commit_index"], 10, 64)
	status.AppliedIndex = node.raft.AppliedIndex()
	status.LeaderID, status.LeaderAddress, status.LeaderKnown = node.Leader()
	if status.LeaderKnown {
		status.LeaderAPIAddress, _ = node.LeaderAPIAddress(status.LeaderID)
	}
	if node.raft.State() != raft.Leader {
		return status
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if node.RequireMutationAuthority(ctx) == nil {
		status.QuorumConfirmed = true
		status.MutationAuthority = true
	}
	return status
}

func waitFuture(ctx context.Context, future raft.Future) error {
	result := make(chan error, 1)
	go func() { result <- future.Error() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (node *Node) RequireMutationAuthority(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("mutation authority context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node == nil || node.raft == nil || node.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	if err := waitFuture(ctx, node.raft.VerifyLeader()); err != nil {
		return fmt.Errorf("%w: %v", ErrNoQuorum, err)
	}
	return nil
}

func (node *Node) Synchronize() error {
	return node.synchronize(true)
}

// SynchronizeForCommit drains inherited Raft entries once per leadership
// term. Subsequent repository commits in the same term can rely on Commit's
// quorum verification and Apply ordering without adding another barrier.
func (node *Node) SynchronizeForCommit() error {
	return node.synchronize(false)
}

func (node *Node) synchronize(force bool) error {
	if node == nil || node.raft == nil {
		return ErrNotLeader
	}
	node.synchronizeMu.Lock()
	defer node.synchronizeMu.Unlock()
	if node.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	term := node.raft.CurrentTerm()
	if !force && term != 0 && node.synchronizedCommitTerm == term {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), node.applyTimeout)
	defer cancel()
	if err := node.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	term = node.raft.CurrentTerm()
	if err := waitFuture(ctx, node.raft.Barrier(node.applyTimeout)); err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			return ErrNotLeader
		}
		return fmt.Errorf("%w: synchronize applied Raft state: %v", ErrCommitIndeterminate, err)
	}
	if node.raft.State() != raft.Leader || node.raft.CurrentTerm() != term {
		return ErrNotLeader
	}
	node.synchronizedCommitTerm = term
	return nil
}

func (node *Node) Commit(state []byte) error {
	if node == nil || node.raft == nil {
		return ErrNotLeader
	}
	if len(state) == 0 || len(state) > maximumReplicatedStateBytes {
		return fmt.Errorf("replicated state size is invalid")
	}
	if err := node.fsm.machine.ValidateReplicatedState(state); err != nil {
		return err
	}
	node.commitMu.Lock()
	defer node.commitMu.Unlock()
	command, err := encodeReplicatedLog(state)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), node.applyTimeout)
	defer cancel()
	if err := node.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	future := node.raft.Apply(command, node.applyTimeout)
	err = future.Error()
	if err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			return ErrNotLeader
		}
		return fmt.Errorf("%w: %v", ErrCommitIndeterminate, err)
	}
	if responseErr, ok := future.Response().(error); ok && responseErr != nil {
		return fmt.Errorf("apply committed Raft state: %w", responseErr)
	}
	return nil
}

func (node *Node) Leader() (model.ResourceID, string, bool) {
	if node == nil || node.raft == nil {
		return "", "", false
	}
	address, id := node.raft.LeaderWithID()
	resourceID := model.ResourceID(id)
	return resourceID, string(address), model.ValidResourceID(resourceID) && address != ""
}

func (node *Node) LeaderAPIAddress(controllerID model.ResourceID) (string, bool) {
	if node == nil || !model.ValidResourceID(controllerID) {
		return "", false
	}
	address := strings.TrimSpace(node.leaderAPIs[controllerID])
	return address, address != ""
}

func (node *Node) Close() error {
	if node == nil {
		return nil
	}
	node.closeOnce.Do(func() {
		if node.raft != nil {
			node.closeErr = node.raft.Shutdown().Error()
		}
		if node.transport != nil {
			node.closeErr = errors.Join(node.closeErr, node.transport.Close())
		}
		if node.store != nil {
			node.closeErr = errors.Join(node.closeErr, node.store.Close())
		}
	})
	return node.closeErr
}
