package consensus

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
	"github.com/hashicorp/raft"
)

func writeTestRaftTLSIdentity(t *testing.T) (string, string, string) {
	return writeTestRaftTLSIdentityWith(t,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		[]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"},
	)
}

func writeTestRaftTLSIdentityWith(t *testing.T, usages []x509.ExtKeyUsage, addresses []net.IP, names []string) (string, string, string) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate Raft test CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ClusterGuard test Raft CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create Raft test CA: %v", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate Raft test leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "clusterguard-controller"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: usages, IPAddresses: addresses, DNSNames: names,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create Raft test leaf certificate: %v", err)
	}
	directory := t.TempDir()
	caPath := filepath.Join(directory, "ca.pem")
	certPath := filepath.Join(directory, "controller.pem")
	keyPath := filepath.Join(directory, "controller-key.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("write Raft test CA: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o600); err != nil {
		t.Fatalf("write Raft test certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}), 0o600); err != nil {
		t.Fatalf("write Raft test key: %v", err)
	}
	return certPath, keyPath, caPath
}

func TestRaftTLSIdentityRequiresClientAndServerUsage(t *testing.T) {
	certPath, keyPath, caPath := writeTestRaftTLSIdentityWith(t,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		[]net.IP{net.ParseIP("127.0.0.1")}, nil,
	)
	peers := []Peer{
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10009"},
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10010"},
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10011"},
	}
	err := ValidateConfiguration(Config{
		LocalID: peers[0].ResourceID, BindAddress: peers[0].Address, AdvertiseAddress: peers[0].Address,
		DataDirectory: t.TempDir(), Peers: peers, TLSCertFile: certPath, TLSKeyFile: keyPath, TLSCAFile: caPath,
	})
	if err == nil || !strings.Contains(err.Error(), "client authentication") {
		t.Fatalf("server-only Raft identity error=%v", err)
	}
}

func TestRaftTLSIdentityRequiresAdvertisedAddress(t *testing.T) {
	certPath, keyPath, caPath := writeTestRaftTLSIdentityWith(t,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		[]net.IP{net.ParseIP("127.0.0.2")}, nil,
	)
	peers := []Peer{
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10009"},
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10010"},
		{ResourceID: model.NewResourceID(), Address: "127.0.0.1:10011"},
	}
	err := ValidateConfiguration(Config{
		LocalID: peers[0].ResourceID, BindAddress: peers[0].Address, AdvertiseAddress: peers[0].Address,
		DataDirectory: t.TempDir(), Peers: peers, TLSCertFile: certPath, TLSKeyFile: keyPath, TLSCAFile: caPath,
	})
	if err == nil || !strings.Contains(err.Error(), "advertised address") {
		t.Fatalf("wrong-address Raft identity error=%v", err)
	}
}

type stateRecorder struct {
	mu     sync.Mutex
	states [][]byte
}

type rejectingStateRecorder struct {
	stateRecorder
	reject string
}

type committedApplyWarning struct{}

func (committedApplyWarning) Error() string   { return "metadata directory sync warning" }
func (committedApplyWarning) Committed() bool { return true }

type warningStateRecorder struct{ stateRecorder }

type selfValidatingStateRecorder struct {
	stateRecorder
	validateCalls int
	applyCalls    int
}

func (*selfValidatingStateRecorder) ApplyValidatesReplicatedState() bool { return true }

func (recorder *selfValidatingStateRecorder) ValidateReplicatedState(state []byte) error {
	recorder.validateCalls++
	return recorder.stateRecorder.ValidateReplicatedState(state)
}

func (recorder *selfValidatingStateRecorder) ApplyReplicatedState(state []byte) error {
	recorder.applyCalls++
	if err := recorder.ValidateReplicatedState(state); err != nil {
		return err
	}
	return recorder.stateRecorder.ApplyReplicatedState(state)
}

func (recorder *warningStateRecorder) ApplyReplicatedState(state []byte) error {
	if err := recorder.stateRecorder.ApplyReplicatedState(state); err != nil {
		return err
	}
	return committedApplyWarning{}
}

func (recorder *rejectingStateRecorder) ApplyReplicatedState(state []byte) error {
	if string(state) == recorder.reject {
		return context.Canceled
	}
	return recorder.stateRecorder.ApplyReplicatedState(state)
}

func (recorder *stateRecorder) ValidateReplicatedState(state []byte) error {
	if len(state) == 0 {
		return context.Canceled
	}
	return nil
}

func (recorder *stateRecorder) ApplyReplicatedState(state []byte) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.states = append(recorder.states, append([]byte{}, state...))
	return nil
}

func (recorder *stateRecorder) contains(want string) bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, state := range recorder.states {
		if string(state) == want {
			return true
		}
	}
	return false
}

func TestReplicatedFSMNeverSuppressesCommittedState(t *testing.T) {
	recorder := &stateRecorder{}
	fsm := &replicatedFSM{machine: recorder}

	foreignState := []byte(`{"revision":2}`)
	foreignCommand, err := encodeReplicatedLog(foreignState)
	if err != nil {
		t.Fatalf("encode foreign command: %v", err)
	}
	if response := fsm.Apply(&raft.Log{Data: foreignCommand}); response != nil {
		t.Fatalf("apply foreign command: %v", response)
	}
	if !recorder.contains(string(foreignState)) {
		t.Fatal("foreign leader command was skipped by a pending local commit")
	}

	localState := []byte(`{"revision":1}`)
	localCommand, err := encodeReplicatedLog(localState)
	if err != nil {
		t.Fatalf("encode local command: %v", err)
	}
	if response := fsm.Apply(&raft.Log{Data: localCommand}); response != nil {
		t.Fatalf("apply matching local command: %v", response)
	}
	if !recorder.contains(string(localState)) {
		t.Fatal("matching local command was suppressed instead of applying through the state machine")
	}

	if response := fsm.Apply(&raft.Log{Data: localCommand}); response != nil {
		t.Fatalf("reapply committed local command: %v", response)
	}
	if !recorder.contains(string(localState)) {
		t.Fatal("replayed local command was not applied")
	}
}

func TestReplicatedFSMDoesNotRepeatValidationForSelfValidatingApply(t *testing.T) {
	recorder := &selfValidatingStateRecorder{}
	fsm := &replicatedFSM{machine: recorder}
	state := []byte(`{"revision":1}`)
	if response := fsm.Apply(&raft.Log{Data: state}); response != nil {
		t.Fatalf("apply self-validating state: %v", response)
	}
	if recorder.applyCalls != 1 || recorder.validateCalls != 1 {
		t.Fatalf("apply calls=%d validate calls=%d, want one of each", recorder.applyCalls, recorder.validateCalls)
	}
}

func TestReplicatedLogRemainsReadableByLegacySnapshotDecoder(t *testing.T) {
	state := []byte(`{"clusters":{"cluster-1":{"display_name":"mysql-ha"}},"nodes":{},"instances":{}}`)
	command, err := encodeReplicatedLog(state)
	if err != nil {
		t.Fatalf("encode replicated log: %v", err)
	}
	legacy := struct {
		Clusters map[string]json.RawMessage `json:"clusters"`
	}{}
	if err := json.Unmarshal(command, &legacy); err != nil {
		t.Fatalf("decode replicated log with legacy snapshot decoder: %v", err)
	}
	if len(legacy.Clusters) != 1 {
		t.Fatalf("legacy decoder saw clusters=%v, want the original snapshot payload", legacy.Clusters)
	}
}

func TestReplicatedFSMDoesNotSnapshotRejectedState(t *testing.T) {
	recorder := &rejectingStateRecorder{reject: `{"revision":2}`}
	fsm := &replicatedFSM{machine: recorder}
	accepted := []byte(`{"revision":1}`)
	if response := fsm.Apply(&raft.Log{Data: accepted}); response != nil {
		t.Fatalf("apply accepted state: %v", response)
	}
	rejected := []byte(recorder.reject)
	if response := fsm.Apply(&raft.Log{Data: rejected}); response == nil {
		t.Fatal("state-machine rejection was ignored")
	}
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	if string(fsm.state) != string(accepted) {
		t.Fatalf("snapshot state=%s, want last accepted state=%s", fsm.state, accepted)
	}
}

func TestReplicatedFSMSnapshotsStateCommittedWithDurabilityWarning(t *testing.T) {
	recorder := &warningStateRecorder{}
	fsm := &replicatedFSM{machine: recorder}
	state := []byte(`{"revision":7}`)
	response := fsm.Apply(&raft.Log{Data: state})
	if _, ok := response.(error); !ok {
		t.Fatalf("committed durability warning response=%v, want error", response)
	}
	fsm.mu.RLock()
	defer fsm.mu.RUnlock()
	if string(fsm.state) != string(state) {
		t.Fatalf("snapshot state=%s, want committed state=%s", fsm.state, state)
	}
}

func TestNodeReportsExplicitSnapshotCASProtocolState(t *testing.T) {
	disabled := &Node{}
	if disabled.SnapshotCASActive() {
		t.Fatal("snapshot CAS protocol was active without explicit configuration")
	}
	enabled := &Node{snapshotCAS: true}
	if !enabled.SnapshotCASActive() {
		t.Fatal("snapshot CAS protocol did not report the configured active state")
	}
}

func TestNodeResolvesLeaderAPIOnlyFromConfiguredControllerIdentity(t *testing.T) {
	leaderID := model.NewResourceID()
	node := &Node{leaderAPIs: map[model.ResourceID]string{leaderID: "https://controller-a.example:3000"}}
	address, found := node.LeaderAPIAddress(leaderID)
	if !found || address != "https://controller-a.example:3000" {
		t.Fatalf("leader API address=%q found=%t", address, found)
	}
	if address, found := node.LeaderAPIAddress(model.NewResourceID()); found || address != "" {
		t.Fatalf("unknown controller API address=%q found=%t", address, found)
	}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate TCP address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP address: %v", err)
	}
	return address
}

func TestRaftTLSStreamLayerRequiresClientCertificatesAndTransfersData(t *testing.T) {
	certPath, keyPath, caPath := writeTestRaftTLSIdentity(t)
	address := freeTCPAddress(t)
	advertise, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		t.Fatalf("resolve Raft TLS advertise address: %v", err)
	}
	layer, err := newRaftTLSStreamLayer(address, advertise, certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("open Raft TLS stream layer: %v", err)
	}
	defer layer.Close()
	if layer.serverTLS.ClientAuth != tls.RequireAndVerifyClientCert || layer.clientTLS.RootCAs == nil || len(layer.clientTLS.Certificates) != 1 {
		t.Fatalf("Raft TLS is not mutual: server=%+v client=%+v", layer.serverTLS, layer.clientTLS)
	}
	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := layer.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer connection.Close()
		buffer := make([]byte, 4)
		if _, readErr := io.ReadFull(connection, buffer); readErr != nil {
			serverResult <- readErr
			return
		}
		if string(buffer) != "raft" {
			serverResult <- fmt.Errorf("server received %q", buffer)
			return
		}
		serverResult <- nil
	}()
	connection, err := layer.Dial(raft.ServerAddress(address), time.Second)
	if err != nil {
		t.Fatalf("dial Raft TLS stream layer: %v", err)
	}
	if _, err := connection.Write([]byte("raft")); err != nil {
		t.Fatalf("write Raft TLS payload: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close Raft TLS client: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("Raft TLS server: %v", err)
	}
}

func TestRaftConfigurationRejectsExternalPlaintextTransport(t *testing.T) {
	peers := []Peer{
		{ResourceID: model.NewResourceID(), Address: "192.0.2.11:10009"},
		{ResourceID: model.NewResourceID(), Address: "192.0.2.12:10009"},
		{ResourceID: model.NewResourceID(), Address: "192.0.2.13:10009"},
	}
	configuration := Config{
		LocalID: peers[0].ResourceID, BindAddress: "0.0.0.0:10009", AdvertiseAddress: peers[0].Address,
		DataDirectory: t.TempDir(), Peers: peers,
	}
	if err := validateConfig(configuration); err == nil || !strings.Contains(err.Error(), "Raft TLS") {
		t.Fatalf("external plaintext Raft configuration error=%v", err)
	}
	configuration.AllowInsecureTransport = true
	if err := validateConfig(configuration); err != nil {
		t.Fatalf("explicit insecure Raft configuration rejected: %v", err)
	}
}

func waitForRaftLeader(t *testing.T, nodes []*Node) *Node {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		leaders := make([]*Node, 0, 1)
		for _, node := range nodes {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			err := node.RequireMutationAuthority(ctx)
			cancel()
			if err == nil {
				leaders = append(leaders, node)
			}
		}
		if len(leaders) == 1 {
			return leaders[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("three-node Raft cluster did not elect one authoritative leader")
	return nil
}

func TestThreeNodeRaftCommitsToFollowersAndLosesAuthorityWithoutQuorum(t *testing.T) {
	addresses := []string{freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t)}
	peers := make([]Peer, 3)
	for index := range peers {
		peers[index] = Peer{ResourceID: model.NewResourceID(), Address: addresses[index]}
	}
	recorders := []*stateRecorder{{}, {}, {}}
	nodes := make([]*Node, 3)
	for _, index := range []int{1, 2, 0} {
		node, err := Open(Config{
			LocalID: peers[index].ResourceID, BindAddress: addresses[index], AdvertiseAddress: addresses[index],
			DataDirectory: filepath.Join(t.TempDir(), "raft"), Peers: peers, Bootstrap: index == 0,
			ApplyTimeout: 3 * time.Second,
		}, recorders[index])
		if err != nil {
			t.Fatalf("open Raft node %d: %v", index, err)
		}
		nodes[index] = node
		defer node.Close()
	}
	leader := waitForRaftLeader(t, nodes)
	statusContext, cancelStatus := context.WithTimeout(context.Background(), time.Second)
	leaderStatus := leader.Status(statusContext)
	cancelStatus()
	if !leaderStatus.Enabled || leaderStatus.Role != "leader" || !leaderStatus.LeaderKnown ||
		!leaderStatus.QuorumConfirmed || !leaderStatus.MutationAuthority || leaderStatus.VoterCount != 3 ||
		leaderStatus.LocalControllerID == "" || leaderStatus.LeaderID != leaderStatus.LocalControllerID ||
		leaderStatus.Term == 0 || leaderStatus.SnapshotCASActive {
		t.Fatalf("leader status=%+v", leaderStatus)
	}
	for _, node := range nodes {
		if node == leader {
			continue
		}
		followerStatus := node.Status(context.Background())
		if followerStatus.Role != "follower" || !followerStatus.LeaderKnown || followerStatus.LeaderID != leaderStatus.LeaderID ||
			followerStatus.MutationAuthority || followerStatus.QuorumConfirmed || followerStatus.VoterCount != 3 {
			t.Fatalf("follower status=%+v leader=%+v", followerStatus, leaderStatus)
		}
		break
	}
	if err := leader.Commit([]byte(`{"revision":1}`)); err != nil {
		t.Fatalf("commit replicated state: %v", err)
	}
	if err := leader.Synchronize(); err != nil {
		t.Fatalf("synchronize committed state: %v", err)
	}
	synchronizedIndex := leader.raft.LastIndex()
	if err := leader.SynchronizeForCommit(); err != nil {
		t.Fatalf("reuse term-scoped commit synchronization: %v", err)
	}
	if current := leader.raft.LastIndex(); current != synchronizedIndex {
		t.Fatalf("same-term commit synchronization appended another barrier: index=%d want=%d", current, synchronizedIndex)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		applied := 0
		for index, node := range nodes {
			if node != leader && recorders[index].contains(`{"revision":1}`) {
				applied++
			}
		}
		if applied == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for index, node := range nodes {
		if node != leader && !recorders[index].contains(`{"revision":1}`) {
			t.Fatalf("follower %d did not apply committed state", index)
		}
	}

	stopped := 0
	for _, node := range nodes {
		if node == leader || stopped >= 2 {
			continue
		}
		if err := node.Close(); err != nil {
			t.Fatalf("close follower: %v", err)
		}
		stopped++
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := leader.RequireMutationAuthority(ctx)
		cancel()
		if err != nil {
			if synchronizeErr := leader.Synchronize(); synchronizeErr == nil {
				t.Fatal("isolated leader synchronized state without a majority")
			}
			if commitSyncErr := leader.SynchronizeForCommit(); commitSyncErr == nil {
				if commitErr := leader.Commit([]byte(`{"revision":2}`)); commitErr == nil {
					t.Fatal("cached same-term synchronization allowed a commit without a majority")
				}
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated leader retained mutation authority without a majority")
}

func TestThreeNodeRaftCommitsOverMutualTLS(t *testing.T) {
	certPath, keyPath, caPath := writeTestRaftTLSIdentity(t)
	addresses := []string{freeTCPAddress(t), freeTCPAddress(t), freeTCPAddress(t)}
	peers := make([]Peer, 3)
	for index := range peers {
		peers[index] = Peer{ResourceID: model.NewResourceID(), Address: addresses[index]}
	}
	recorders := []*stateRecorder{{}, {}, {}}
	nodes := make([]*Node, 3)
	for _, index := range []int{1, 2, 0} {
		node, err := Open(Config{
			LocalID: peers[index].ResourceID, BindAddress: addresses[index], AdvertiseAddress: addresses[index],
			DataDirectory: filepath.Join(t.TempDir(), "raft"), Peers: peers, Bootstrap: index == 0,
			ApplyTimeout: 3 * time.Second, TLSCertFile: certPath, TLSKeyFile: keyPath, TLSCAFile: caPath,
		}, recorders[index])
		if err != nil {
			t.Fatalf("open TLS Raft node %d: %v", index, err)
		}
		nodes[index] = node
		defer node.Close()
	}
	leader := waitForRaftLeader(t, nodes)
	if err := leader.Commit([]byte(`{"revision":9}`)); err != nil {
		t.Fatalf("commit state over mutual TLS: %v", err)
	}
	if err := leader.Synchronize(); err != nil {
		t.Fatalf("synchronize state over mutual TLS: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		followersReady := 0
		for index, node := range nodes {
			if node != leader && recorders[index].contains(`{"revision":9}`) {
				followersReady++
			}
		}
		if followersReady == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mutual-TLS Raft followers did not apply committed state")
}

func TestRaftConfigurationRequiresOddUniqueThreeNodeMembership(t *testing.T) {
	peer := Peer{ResourceID: model.NewResourceID(), Address: freeTCPAddress(t)}
	for _, peers := range [][]Peer{{peer}, {peer, peer}} {
		if _, err := Open(Config{LocalID: peer.ResourceID, BindAddress: peer.Address, AdvertiseAddress: peer.Address, DataDirectory: t.TempDir(), Peers: peers}, &stateRecorder{}); err == nil {
			t.Fatalf("unsafe membership was accepted: %+v", peers)
		}
	}
}

func TestRaftRuntimeConfigurationCompactsFullStateLogsAggressively(t *testing.T) {
	localID := model.NewResourceID()
	configuration := newRaftRuntimeConfiguration(localID)

	if string(configuration.LocalID) != string(localID) {
		t.Fatalf("local id=%q, want %q", configuration.LocalID, localID)
	}
	if configuration.SnapshotThreshold != 64 {
		t.Fatalf("snapshot threshold=%d, want 64", configuration.SnapshotThreshold)
	}
	if configuration.TrailingLogs != 32 {
		t.Fatalf("trailing logs=%d, want 32", configuration.TrailingLogs)
	}
	if configuration.SnapshotInterval != 30*time.Second {
		t.Fatalf("snapshot interval=%s, want 30s", configuration.SnapshotInterval)
	}
}
