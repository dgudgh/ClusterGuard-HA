package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clusterguard.io/ha/pkg/model"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

var (
	ErrNotLeader           = errors.New("local controller is not the Raft leader")
	ErrNoQuorum            = errors.New("Raft leader cannot confirm controller quorum")
	ErrCommitIndeterminate = errors.New("Raft commit outcome is indeterminate")
)

const maximumReplicatedStateBytes = 64 << 20

type Peer struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
}

type Config struct {
	LocalID          model.ResourceID
	BindAddress      string
	AdvertiseAddress string
	DataDirectory    string
	Peers            []Peer
	Bootstrap        bool
	ApplyTimeout     time.Duration
}

type StateMachine interface {
	ValidateReplicatedState([]byte) error
	ApplyReplicatedState([]byte) error
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
	return nil
}

type replicatedFSM struct {
	machine   StateMachine
	skipLocal *atomic.Bool
	mu        sync.RWMutex
	state     []byte
}

func (fsm *replicatedFSM) Apply(log *raft.Log) interface{} {
	state := append([]byte{}, log.Data...)
	if err := fsm.machine.ValidateReplicatedState(state); err != nil {
		return err
	}
	fsm.mu.Lock()
	fsm.state = state
	fsm.mu.Unlock()
	if fsm.skipLocal.Load() {
		return nil
	}
	return fsm.machine.ApplyReplicatedState(state)
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
	if err := fsm.machine.ValidateReplicatedState(state); err != nil {
		return err
	}
	if err := fsm.machine.ApplyReplicatedState(state); err != nil {
		return err
	}
	fsm.mu.Lock()
	fsm.state = append([]byte{}, state...)
	fsm.mu.Unlock()
	return nil
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
	raft         *raft.Raft
	transport    *raft.NetworkTransport
	store        *raftboltdb.BoltStore
	fsm          *replicatedFSM
	applyTimeout time.Duration
	skipLocal    atomic.Bool
	commitMu     sync.Mutex
	closeOnce    sync.Once
	closeErr     error
}

func Open(configuration Config, machine StateMachine) (*Node, error) {
	if machine == nil {
		return nil, fmt.Errorf("Raft state machine is required")
	}
	if err := validateConfig(configuration); err != nil {
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
	transport, err := raft.NewTCPTransport(configuration.BindAddress, advertise, 5, 10*time.Second, io.Discard)
	if err != nil {
		_ = boltStore.Close()
		return nil, fmt.Errorf("open Raft transport: %w", err)
	}
	node := &Node{transport: transport, store: boltStore, applyTimeout: configuration.ApplyTimeout}
	node.fsm = &replicatedFSM{machine: machine, skipLocal: &node.skipLocal}
	raftConfiguration := raft.DefaultConfig()
	raftConfiguration.LocalID = raft.ServerID(configuration.LocalID)
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
	ctx, cancel := context.WithTimeout(context.Background(), node.applyTimeout)
	defer cancel()
	if err := node.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	node.skipLocal.Store(true)
	future := node.raft.Apply(append([]byte{}, state...), node.applyTimeout)
	err := future.Error()
	node.skipLocal.Store(false)
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
