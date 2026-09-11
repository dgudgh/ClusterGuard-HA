package consensus

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"clusterguard.io/ha/internal/controlstate"
	"clusterguard.io/ha/pkg/model"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	bolt "go.etcd.io/bbolt"
)

var (
	ErrNotLeader           = errors.New("local controller is not the Raft leader")
	ErrNoQuorum            = errors.New("Raft leader cannot confirm controller quorum")
	ErrCommitIndeterminate = errors.New("Raft commit outcome is indeterminate")
)

const maximumReplicatedStateBytes = controlstate.MaximumBytes

const (
	raftSnapshotThreshold             = 64
	raftTrailingLogs                  = 32
	raftSnapshotInterval              = 30 * time.Second
	raftStoreCompactionThresholdBytes = 256 << 20
	raftStoreCompactionTransactionMax = 64 << 20
)

var replicatedLogCompressedMagic = []byte{'C', 'G', 'H', 'A', 'R', 'A', 'F', 'T', 1}

// Hashicorp Raft futures do not expose cancellation. A request can therefore
// time out while its future still completes in the background. Bound those
// waiters so repeated timed-out client requests cannot grow goroutines without
// limit while the local Raft transport is unhealthy.
var futureWaiterSlots = make(chan struct{}, 64)

type Peer struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
	APIAddress string           `json:"api_address,omitempty"`
}

// ControllerMember is the durable identity and network location used when
// changing the live Raft voter set. ResourceID is immutable; addresses may be
// reconciled during a controlled replacement.
type ControllerMember struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
	APIAddress string           `json:"api_address,omitempty"`
}

type Config struct {
	LocalID                         model.ResourceID
	BindAddress                     string
	AdvertiseAddress                string
	DataDirectory                   string
	Peers                           []Peer
	Bootstrap                       bool
	ApplyTimeout                    time.Duration
	SnapshotCASEnabled              bool
	ReplicatedLogCompressionEnabled bool
	AllowInsecureTransport          bool
	TLSCertFile                     string
	TLSKeyFile                      string
	TLSCAFile                       string
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

func encodeReplicatedLog(state []byte, compression ...bool) ([]byte, error) {
	if len(state) == 0 || len(state) > maximumReplicatedStateBytes {
		return nil, fmt.Errorf("replicated state is invalid")
	}
	if len(compression) == 0 || !compression[0] {
		return append([]byte{}, state...), nil
	}
	var encoded bytes.Buffer
	encoded.Write(replicatedLogCompressedMagic)
	writer, err := flate.NewWriter(&encoded, flate.BestSpeed)
	if err != nil {
		return nil, fmt.Errorf("initialize replicated state compression: %w", err)
	}
	if _, err := writer.Write(state); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("compress replicated state: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish replicated state compression: %w", err)
	}
	if encoded.Len() >= len(state) {
		return append([]byte{}, state...), nil
	}
	return encoded.Bytes(), nil
}

func decodeCompressedReplicatedLog(contents []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(contents[len(replicatedLogCompressedMagic):]))
	state, readErr := io.ReadAll(io.LimitReader(reader, maximumReplicatedStateBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("decompress replicated state: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close replicated state decompressor: %w", closeErr)
	}
	if len(state) == 0 || len(state) > maximumReplicatedStateBytes {
		return nil, fmt.Errorf("decompressed replicated state size is invalid")
	}
	return append([]byte{}, state...), nil
}

func decodeReplicatedLog(contents []byte) ([]byte, error) {
	if len(contents) == 0 || len(contents) > maximumReplicatedStateBytes {
		return nil, fmt.Errorf("replicated state size is invalid")
	}
	if bytes.HasPrefix(contents, replicatedLogCompressedMagic) {
		if len(contents) == len(replicatedLogCompressedMagic) {
			return nil, fmt.Errorf("compressed replicated state is empty")
		}
		return decodeCompressedReplicatedLog(contents)
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
	configuredVoterCount   int
	applyTimeout           time.Duration
	snapshotCAS            bool
	compressReplicatedLog  bool
	leaderAPIs             map[model.ResourceID]string
	leaderAPIsMu           sync.RWMutex
	leaderAPIScheme        string
	leaderAPIPort          string
	membershipMu           sync.Mutex
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

type boltStoreCopier struct {
	destination *bolt.DB
	transaction *bolt.Tx
	bytes       int64
	maxBytes    int64
}

type raftTailRepair struct {
	Repaired          bool
	RemovedIndex      uint64
	FirstRemovedIndex uint64
	RemovedCount      int
	RetainedIndex     uint64
	BackupPath        string
}

func raftLogIndex(key []byte) (uint64, error) {
	if len(key) != 8 {
		return 0, fmt.Errorf("Raft log key has %d bytes, want 8", len(key))
	}
	return binary.BigEndian.Uint64(key), nil
}

func raftTailKeySuffix(path string, limit int) ([][]byte, error) {
	database, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer database.Close()
	keys := make([][]byte, 0, limit)
	err = database.View(func(transaction *bolt.Tx) error {
		bucket := transaction.Bucket([]byte("logs"))
		if bucket == nil {
			return fmt.Errorf("Raft logs bucket is missing")
		}
		cursor := bucket.Cursor()
		for key, _ := cursor.Last(); key != nil && len(keys) < limit; key, _ = cursor.Prev() {
			keys = append(keys, append([]byte{}, key...))
		}
		return nil
	})
	return keys, err
}

func readRaftLog(path string, index uint64) (raft.Log, error) {
	store, err := raftboltdb.New(raftboltdb.Options{
		Path: path, BoltOptions: &bolt.Options{ReadOnly: true, Timeout: time.Second},
	})
	if err != nil {
		return raft.Log{}, err
	}
	defer store.Close()
	entry := raft.Log{}
	if err := store.GetLog(index, &entry); err != nil {
		return raft.Log{}, err
	}
	if entry.Index != index {
		return raft.Log{}, fmt.Errorf("Raft log payload index %d does not match key index %d", entry.Index, index)
	}
	return entry, nil
}

func backupRaftStore(path string, now func() time.Time) (string, error) {
	if now == nil {
		now = time.Now
	}
	backupPath := fmt.Sprintf("%s.corrupt-tail-%s.bak", path, now().UTC().Format("20060102T150405.000000000Z"))
	source, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return "", fmt.Errorf("open Raft store for backup: %w", err)
	}
	destination, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = source.Close()
		return "", fmt.Errorf("create Raft repair backup: %w", err)
	}
	writeErr := source.View(func(transaction *bolt.Tx) error {
		_, err := transaction.WriteTo(destination)
		return err
	})
	syncErr := destination.Sync()
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	if writeErr != nil || syncErr != nil || closeDestinationErr != nil || closeSourceErr != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("write Raft repair backup: %w", errors.Join(writeErr, syncErr, closeDestinationErr, closeSourceErr))
	}
	if err := syncMetadataDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("sync Raft repair backup: %w", err)
	}
	return backupPath, nil
}

func syncMetadataDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func repairRaftOutlierTail(path string, trustedSnapshotIndex uint64, now func() time.Time) (raftTailRepair, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return raftTailRepair{}, nil
	} else if err != nil {
		return raftTailRepair{}, fmt.Errorf("inspect Raft store: %w", err)
	}
	keys, err := raftTailKeySuffix(path, 256)
	if err != nil {
		return raftTailRepair{}, fmt.Errorf("inspect Raft log tail: %w", err)
	}
	if len(keys) == 0 {
		return raftTailRepair{}, nil
	}
	lastKey := keys[0]
	lastIndex, keyErr := raftLogIndex(lastKey)
	if keyErr == nil {
		if _, logErr := readRaftLog(path, lastIndex); logErr == nil {
			return raftTailRepair{}, nil
		} else {
			err = logErr
		}
	} else {
		err = keyErr
	}
	corruptKeys := [][]byte{lastKey}
	corruptIndexes := []uint64{lastIndex}
	retainedIndex := uint64(0)
	for _, key := range keys[1:] {
		index, indexErr := raftLogIndex(key)
		if indexErr != nil {
			return raftTailRepair{}, fmt.Errorf("Raft tail contains a malformed key below corruption: %w", indexErr)
		}
		if _, logErr := readRaftLog(path, index); logErr == nil {
			retainedIndex = index
			break
		}
		corruptKeys = append(corruptKeys, key)
		corruptIndexes = append(corruptIndexes, index)
	}
	if retainedIndex == 0 {
		return raftTailRepair{}, fmt.Errorf("Raft tail is corrupt and no valid predecessor was found within %d entries: %w", len(keys), err)
	}
	contiguous := false
	firstRemovedIndex := corruptIndexes[0]
	for _, index := range corruptIndexes {
		if index <= retainedIndex {
			return raftTailRepair{}, fmt.Errorf("Raft corrupt suffix crosses retained index %d", retainedIndex)
		}
		if index < firstRemovedIndex {
			firstRemovedIndex = index
		}
		if index == retainedIndex+1 {
			contiguous = true
		}
	}
	if contiguous && trustedSnapshotIndex == 0 {
		return raftTailRepair{}, fmt.Errorf("Raft tail at contiguous index %d is corrupt; automatic truncation is refused without a validated snapshot: %w", retainedIndex+1, err)
	}
	if trustedSnapshotIndex > retainedIndex {
		return raftTailRepair{}, fmt.Errorf("Raft valid predecessor index %d is older than validated snapshot index %d", retainedIndex, trustedSnapshotIndex)
	}
	backupPath, backupErr := backupRaftStore(path, now)
	if backupErr != nil {
		return raftTailRepair{}, backupErr
	}
	database, openErr := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if openErr != nil {
		return raftTailRepair{}, fmt.Errorf("open Raft store for outlier repair: %w", openErr)
	}
	deleteErr := database.Update(func(transaction *bolt.Tx) error {
		bucket := transaction.Bucket([]byte("logs"))
		if bucket == nil {
			return fmt.Errorf("Raft logs bucket is missing")
		}
		currentLast, _ := bucket.Cursor().Last()
		if !bytes.Equal(currentLast, lastKey) {
			return fmt.Errorf("Raft log tail changed during repair")
		}
		for _, key := range corruptKeys {
			if err := bucket.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
	syncErr := database.Sync()
	closeErr := database.Close()
	if deleteErr != nil || syncErr != nil || closeErr != nil {
		return raftTailRepair{}, fmt.Errorf("remove corrupt Raft outlier: %w", errors.Join(deleteErr, syncErr, closeErr))
	}
	if directoryErr := syncMetadataDirectory(filepath.Dir(path)); directoryErr != nil {
		return raftTailRepair{}, fmt.Errorf("sync repaired Raft directory: %w", directoryErr)
	}
	return raftTailRepair{
		Repaired: true, RemovedIndex: lastIndex, FirstRemovedIndex: firstRemovedIndex,
		RemovedCount: len(corruptKeys), RetainedIndex: retainedIndex, BackupPath: backupPath,
	}, nil
}

func latestValidatedSnapshotIndex(snapshotStore raft.SnapshotStore, machine StateMachine) (uint64, error) {
	snapshots, err := snapshotStore.List()
	if err != nil {
		return 0, fmt.Errorf("list Raft snapshots: %w", err)
	}
	for _, snapshot := range snapshots {
		_, reader, openErr := snapshotStore.Open(snapshot.ID)
		if openErr != nil {
			continue
		}
		state, readErr := io.ReadAll(io.LimitReader(reader, maximumReplicatedStateBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(state) == 0 || len(state) > maximumReplicatedStateBytes {
			continue
		}
		if validateErr := machine.ValidateReplicatedState(state); validateErr != nil {
			continue
		}
		return snapshot.Index, nil
	}
	if len(snapshots) > 0 {
		return 0, fmt.Errorf("no Raft snapshot passed checksum and replicated-state validation")
	}
	return 0, nil
}

func (copier *boltStoreCopier) flush() error {
	if copier.transaction == nil {
		return nil
	}
	err := copier.transaction.Commit()
	copier.transaction = nil
	copier.bytes = 0
	return err
}

func (copier *boltStoreCopier) rollback() {
	if copier.transaction != nil {
		_ = copier.transaction.Rollback()
		copier.transaction = nil
		copier.bytes = 0
	}
}

func (copier *boltStoreCopier) ensureBucket(path [][]byte, sequence uint64) error {
	if err := copier.flush(); err != nil {
		return err
	}
	return copier.destination.Update(func(transaction *bolt.Tx) error {
		var bucket *bolt.Bucket
		for index, name := range path {
			var err error
			if index == 0 {
				bucket, err = transaction.CreateBucketIfNotExists(name)
			} else {
				bucket, err = bucket.CreateBucketIfNotExists(name)
			}
			if err != nil {
				return err
			}
		}
		return bucket.SetSequence(sequence)
	})
}

func destinationBucket(transaction *bolt.Tx, path [][]byte) *bolt.Bucket {
	bucket := transaction.Bucket(path[0])
	for _, name := range path[1:] {
		if bucket == nil {
			return nil
		}
		bucket = bucket.Bucket(name)
	}
	return bucket
}

func (copier *boltStoreCopier) put(path [][]byte, key, value []byte) error {
	entryBytes := int64(len(key) + len(value))
	if copier.transaction != nil && copier.bytes > 0 && copier.bytes+entryBytes > copier.maxBytes {
		if err := copier.flush(); err != nil {
			return err
		}
	}
	if copier.transaction == nil {
		transaction, err := copier.destination.Begin(true)
		if err != nil {
			return err
		}
		copier.transaction = transaction
	}
	bucket := destinationBucket(copier.transaction, path)
	if bucket == nil {
		return fmt.Errorf("destination bucket %q is missing", bytes.Join(path, []byte("/")))
	}
	if err := bucket.Put(key, value); err != nil {
		return err
	}
	copier.bytes += entryBytes
	return nil
}

func copyBoltBucket(copier *boltStoreCopier, source *bolt.Bucket, path [][]byte) error {
	if err := copier.ensureBucket(path, source.Sequence()); err != nil {
		return err
	}
	cursor := source.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		if value != nil {
			if err := copier.put(path, key, value); err != nil {
				return err
			}
			continue
		}
		if err := copier.flush(); err != nil {
			return err
		}
		child := source.Bucket(key)
		if child == nil {
			return fmt.Errorf("source bucket %q disappeared during compaction", key)
		}
		childPath := append(append([][]byte{}, path...), append([]byte{}, key...))
		if err := copyBoltBucket(copier, child, childPath); err != nil {
			return err
		}
	}
	return copier.flush()
}

func copyBoltStore(destination, source *bolt.DB, transactionMaxBytes int64) error {
	if transactionMaxBytes <= 0 {
		transactionMaxBytes = raftStoreCompactionTransactionMax
	}
	copier := &boltStoreCopier{destination: destination, maxBytes: transactionMaxBytes}
	defer copier.rollback()
	return source.View(func(transaction *bolt.Tx) error {
		if err := transaction.ForEach(func(name []byte, bucket *bolt.Bucket) error {
			return copyBoltBucket(copier, bucket, [][]byte{append([]byte{}, name...)})
		}); err != nil {
			return err
		}
		return copier.flush()
	})
}

func verifyBoltStore(database *bolt.DB) error {
	return database.View(func(transaction *bolt.Tx) error {
		var firstError error
		for err := range transaction.Check() {
			if err != nil && firstError == nil {
				firstError = err
			}
		}
		return firstError
	})
}

func compactRaftStore(path string, minimumBytes int64) (int64, int64, bool, error) {
	stat, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("inspect Raft store for compaction: %w", err)
	}
	before := stat.Size()
	if before < minimumBytes {
		return before, before, false, nil
	}
	temporaryPath := path + ".compact"
	if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
		return before, before, false, fmt.Errorf("remove stale Raft compaction file: %w", err)
	}
	source, err := bolt.Open(path, stat.Mode().Perm(), &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return before, before, false, fmt.Errorf("open Raft store for compaction: %w", err)
	}
	destination, err := bolt.Open(temporaryPath, stat.Mode().Perm(), &bolt.Options{Timeout: time.Second})
	if err != nil {
		_ = source.Close()
		return before, before, false, fmt.Errorf("create compacted Raft store: %w", err)
	}
	compactErr := copyBoltStore(destination, source, raftStoreCompactionTransactionMax)
	verifyErr := verifyBoltStore(destination)
	syncDestinationErr := destination.Sync()
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	if compactErr != nil || verifyErr != nil || syncDestinationErr != nil || closeDestinationErr != nil || closeSourceErr != nil {
		_ = os.Remove(temporaryPath)
		return before, before, false, fmt.Errorf(
			"compact Raft store: %w",
			errors.Join(compactErr, verifyErr, syncDestinationErr, closeDestinationErr, closeSourceErr),
		)
	}
	if err := os.Chmod(temporaryPath, stat.Mode().Perm()); err != nil {
		_ = os.Remove(temporaryPath)
		return before, before, false, fmt.Errorf("preserve compacted Raft store permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return before, before, false, fmt.Errorf("publish compacted Raft store: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return before, before, true, fmt.Errorf("open Raft directory after compaction: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return before, before, true, fmt.Errorf("sync Raft directory after compaction: %w", errors.Join(syncErr, closeErr))
	}
	compacted, err := os.Stat(path)
	if err != nil {
		return before, before, true, fmt.Errorf("inspect compacted Raft store: %w", err)
	}
	return before, compacted.Size(), true, nil
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
	snapshotStore, err := raft.NewFileSnapshotStore(configuration.DataDirectory, 3, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("open Raft snapshot store: %w", err)
	}
	trustedSnapshotIndex, err := latestValidatedSnapshotIndex(snapshotStore, machine)
	if err != nil {
		return nil, fmt.Errorf("validate Raft recovery snapshot: %w", err)
	}
	raftStorePath := filepath.Join(configuration.DataDirectory, "raft.db")
	tailRepair, err := repairRaftOutlierTail(raftStorePath, trustedSnapshotIndex, time.Now)
	if err != nil {
		return nil, fmt.Errorf("validate Raft store tail: %w", err)
	}
	if tailRepair.Repaired {
		log.Printf("repaired corrupt Raft suffix indexes %d..%d (%d entries); retained index %d; validated snapshot index %d; backup=%s",
			tailRepair.FirstRemovedIndex, tailRepair.RemovedIndex, tailRepair.RemovedCount,
			tailRepair.RetainedIndex, trustedSnapshotIndex, tailRepair.BackupPath)
	}
	beforeBytes, afterBytes, compacted, err := compactRaftStore(raftStorePath, raftStoreCompactionThresholdBytes)
	if err != nil {
		return nil, err
	}
	if compacted {
		log.Printf("compacted Raft store from %d to %d bytes", beforeBytes, afterBytes)
	}
	boltStore, err := raftboltdb.NewBoltStore(raftStorePath)
	if err != nil {
		return nil, fmt.Errorf("open Raft store: %w", err)
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
		localID: configuration.LocalID, configuredVoterCount: len(configuration.Peers),
		snapshotCAS:           configuration.SnapshotCASEnabled,
		compressReplicatedLog: configuration.ReplicatedLogCompressionEnabled,
		leaderAPIs:            make(map[model.ResourceID]string, len(configuration.Peers)),
	}
	for _, peer := range configuration.Peers {
		node.rememberControllerAPI(peer.ResourceID, peer.APIAddress)
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
	Enabled                        bool             `json:"enabled"`
	LocalControllerID              model.ResourceID `json:"local_controller_id,omitempty"`
	Role                           string           `json:"role"`
	LeaderID                       model.ResourceID `json:"leader_id,omitempty"`
	LeaderAddress                  string           `json:"leader_address,omitempty"`
	LeaderAPIAddress               string           `json:"leader_api_address,omitempty"`
	LeaderKnown                    bool             `json:"leader_known"`
	VoterCount                     int              `json:"voter_count"`
	QuorumConfirmed                bool             `json:"quorum_confirmed"`
	MutationAuthority              bool             `json:"mutation_authority"`
	SnapshotCASActive              bool             `json:"snapshot_cas_active"`
	ReplicatedLogCompressionActive bool             `json:"replicated_log_compression_active"`
	Term                           uint64           `json:"term"`
	LastIndex                      uint64           `json:"last_index"`
	CommitIndex                    uint64           `json:"commit_index"`
	AppliedIndex                   uint64           `json:"applied_index"`
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
	status.VoterCount = node.configuredVoterCount
	if members, err := node.ControllerMembers(ctx); err == nil && len(members) > 0 {
		status.VoterCount = len(members)
	}
	status.SnapshotCASActive = node.snapshotCAS
	status.ReplicatedLogCompressionActive = node.compressReplicatedLog
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
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case futureWaiterSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		defer func() { <-futureWaiterSlots }()
		result <- future.Error()
	}()
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
	command, err := encodeReplicatedLog(state, node.compressReplicatedLog)
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

func validateControllerMember(member ControllerMember) error {
	if !model.ValidResourceID(member.ResourceID) {
		return fmt.Errorf("controller resource UUID is invalid")
	}
	member.Address = strings.TrimSpace(member.Address)
	if _, _, err := net.SplitHostPort(member.Address); err != nil {
		return fmt.Errorf("controller Raft address is invalid")
	}
	if strings.ContainsAny(strings.TrimSpace(member.APIAddress), "\r\n") {
		return fmt.Errorf("controller API address is invalid")
	}
	if address := strings.TrimSpace(member.APIAddress); address != "" {
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return fmt.Errorf("controller API address is invalid")
		}
	}
	return nil
}

func (node *Node) rememberControllerAPI(controllerID model.ResourceID, address string) {
	if node == nil || !model.ValidResourceID(controllerID) {
		return
	}
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if address == "" {
		return
	}
	node.leaderAPIsMu.Lock()
	defer node.leaderAPIsMu.Unlock()
	node.leaderAPIs[controllerID] = address
	if node.leaderAPIScheme != "" {
		return
	}
	parsed, err := url.Parse(address)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
		node.leaderAPIScheme = parsed.Scheme
		node.leaderAPIPort = parsed.Port()
	}
}

// ControllerMembers reads the live Raft configuration. It intentionally does
// not use the static startup peer list, because membership survives restarts
// in Raft's own durable log.
func (node *Node) ControllerMembers(ctx context.Context) ([]ControllerMember, error) {
	if node == nil || node.raft == nil {
		return nil, ErrNotLeader
	}
	if ctx == nil {
		ctx = context.Background()
	}
	future := node.raft.GetConfiguration()
	if err := waitFuture(ctx, future); err != nil {
		return nil, fmt.Errorf("read live Raft membership: %w", err)
	}
	configuration := future.Configuration()
	members := make([]ControllerMember, 0, len(configuration.Servers))
	node.leaderAPIsMu.RLock()
	for _, server := range configuration.Servers {
		if server.Suffrage != raft.Voter {
			continue
		}
		resourceID := model.ResourceID(server.ID)
		members = append(members, ControllerMember{
			ResourceID: resourceID,
			Address:    string(server.Address),
			APIAddress: strings.TrimSpace(node.leaderAPIs[resourceID]),
		})
	}
	node.leaderAPIsMu.RUnlock()
	sort.Slice(members, func(left, right int) bool { return members[left].ResourceID < members[right].ResourceID })
	return members, nil
}

func controllerMembershipIndex(members []ControllerMember) (map[model.ResourceID]ControllerMember, map[string]model.ResourceID) {
	byID := make(map[model.ResourceID]ControllerMember, len(members))
	byAddress := make(map[string]model.ResourceID, len(members))
	for _, member := range members {
		byID[member.ResourceID] = member
		byAddress[member.Address] = member.ResourceID
	}
	return byID, byAddress
}

func (node *Node) rollbackAddedControllerVoters(ctx context.Context, added []model.ResourceID) error {
	var rollbackErr error
	for index := len(added) - 1; index >= 0; index-- {
		future := node.raft.RemoveServer(raft.ServerID(added[index]), 0, node.applyTimeout)
		if err := waitFuture(ctx, future); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback controller %s: %w", added[index], err))
		}
	}
	return rollbackErr
}

// AddControllerVoters adds a complete controller expansion set. The final
// membership must remain an odd set of at least three. If any addition fails,
// voters added by this call are removed before the error is returned.
func (node *Node) AddControllerVoters(ctx context.Context, requested []ControllerMember) error {
	if ctx == nil {
		return fmt.Errorf("controller membership context is required")
	}
	if len(requested) == 0 {
		return nil
	}
	node.membershipMu.Lock()
	defer node.membershipMu.Unlock()
	if err := node.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	current, err := node.ControllerMembers(ctx)
	if err != nil {
		return err
	}
	byID, byAddress := controllerMembershipIndex(current)
	seenIDs := make(map[model.ResourceID]struct{}, len(requested))
	seenAddresses := make(map[string]struct{}, len(requested))
	additions := make([]ControllerMember, 0, len(requested))
	for _, member := range requested {
		member.Address = strings.TrimSpace(member.Address)
		member.APIAddress = strings.TrimRight(strings.TrimSpace(member.APIAddress), "/")
		if err := validateControllerMember(member); err != nil {
			return err
		}
		if _, duplicate := seenIDs[member.ResourceID]; duplicate {
			return fmt.Errorf("controller expansion contains duplicate resource UUID %s", member.ResourceID)
		}
		if _, duplicate := seenAddresses[member.Address]; duplicate {
			return fmt.Errorf("controller expansion contains duplicate Raft address %s", member.Address)
		}
		seenIDs[member.ResourceID] = struct{}{}
		seenAddresses[member.Address] = struct{}{}
		if existing, found := byID[member.ResourceID]; found {
			if existing.Address != member.Address {
				return fmt.Errorf("controller %s is already registered at %s", member.ResourceID, existing.Address)
			}
			node.rememberControllerAPI(member.ResourceID, member.APIAddress)
			continue
		}
		if existingID, found := byAddress[member.Address]; found && existingID != member.ResourceID {
			return fmt.Errorf("Raft address %s already belongs to controller %s", member.Address, existingID)
		}
		additions = append(additions, member)
	}
	finalCount := len(current) + len(additions)
	if finalCount < 3 || finalCount%2 == 0 {
		return fmt.Errorf("final Raft controller membership must be an odd set of at least three voters, got %d", finalCount)
	}
	added := make([]model.ResourceID, 0, len(additions))
	for _, member := range additions {
		future := node.raft.AddVoter(raft.ServerID(member.ResourceID), raft.ServerAddress(member.Address), 0, node.applyTimeout)
		if err := waitFuture(ctx, future); err != nil {
			rollbackErr := node.rollbackAddedControllerVoters(ctx, added)
			return errors.Join(fmt.Errorf("add controller voter %s: %w", member.ResourceID, err), rollbackErr)
		}
		added = append(added, member.ResourceID)
		node.rememberControllerAPI(member.ResourceID, member.APIAddress)
	}
	if err := node.RequireMutationAuthority(ctx); err != nil {
		rollbackErr := node.rollbackAddedControllerVoters(ctx, added)
		return errors.Join(fmt.Errorf("verify controller quorum after expansion: %w", err), rollbackErr)
	}
	updated, err := node.ControllerMembers(ctx)
	if err != nil || len(updated) != finalCount {
		rollbackErr := node.rollbackAddedControllerVoters(ctx, added)
		return errors.Join(fmt.Errorf("verify final controller membership: count=%d want=%d: %w", len(updated), finalCount, err), rollbackErr)
	}
	return nil
}

// RemoveControllerVoters removes a complete retirement set. Removing the
// active leader is rejected so callers can transfer leadership and retry.
func (node *Node) RemoveControllerVoters(ctx context.Context, resourceIDs []model.ResourceID) error {
	if ctx == nil {
		return fmt.Errorf("controller membership context is required")
	}
	if len(resourceIDs) == 0 {
		return nil
	}
	node.membershipMu.Lock()
	defer node.membershipMu.Unlock()
	if err := node.RequireMutationAuthority(ctx); err != nil {
		return err
	}
	current, err := node.ControllerMembers(ctx)
	if err != nil {
		return err
	}
	byID, _ := controllerMembershipIndex(current)
	unique := make([]model.ResourceID, 0, len(resourceIDs))
	seen := make(map[model.ResourceID]struct{}, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		if !model.ValidResourceID(resourceID) {
			return fmt.Errorf("controller resource UUID is invalid")
		}
		if _, duplicate := seen[resourceID]; duplicate {
			return fmt.Errorf("controller retirement contains duplicate resource UUID %s", resourceID)
		}
		seen[resourceID] = struct{}{}
		if _, found := byID[resourceID]; !found {
			continue
		}
		if resourceID == node.localID {
			return fmt.Errorf("active Raft leader cannot remove itself; transfer leadership and retry")
		}
		unique = append(unique, resourceID)
	}
	finalCount := len(current) - len(unique)
	if finalCount < 3 || finalCount%2 == 0 {
		return fmt.Errorf("final Raft controller membership must be an odd set of at least three voters, got %d", finalCount)
	}
	for _, resourceID := range unique {
		future := node.raft.RemoveServer(raft.ServerID(resourceID), 0, node.applyTimeout)
		if err := waitFuture(ctx, future); err != nil {
			return fmt.Errorf("remove controller voter %s: %w", resourceID, err)
		}
		node.leaderAPIsMu.Lock()
		delete(node.leaderAPIs, resourceID)
		node.leaderAPIsMu.Unlock()
	}
	return node.RequireMutationAuthority(ctx)
}

func (node *Node) Leader() (model.ResourceID, string, bool) {
	if node == nil || node.raft == nil {
		return "", "", false
	}
	address, id := node.raft.LeaderWithID()
	resourceID := model.ResourceID(id)
	return resourceID, string(address), model.ValidResourceID(resourceID) && address != ""
}

func (node *Node) LeadershipEpoch() uint64 {
	if node == nil || node.raft == nil {
		return 0
	}
	return node.raft.CurrentTerm()
}

func (node *Node) LeaderAPIAddress(controllerID model.ResourceID) (string, bool) {
	if node == nil || !model.ValidResourceID(controllerID) {
		return "", false
	}
	node.leaderAPIsMu.RLock()
	address := strings.TrimSpace(node.leaderAPIs[controllerID])
	scheme, port := node.leaderAPIScheme, node.leaderAPIPort
	node.leaderAPIsMu.RUnlock()
	if address != "" {
		return address, true
	}
	leaderID, raftAddress, leaderKnown := node.Leader()
	if !leaderKnown || leaderID != controllerID || scheme == "" {
		return "", false
	}
	host, _, err := net.SplitHostPort(raftAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", false
	}
	hostPort := host
	if port != "" {
		hostPort = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		hostPort = "[" + host + "]"
	}
	return (&url.URL{Scheme: scheme, Host: hostPort}).String(), true
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
