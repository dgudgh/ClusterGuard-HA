//go:build gateway_integration

package endpoint_test

import (
	"bufio"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func tcpBackend(t *testing.T, name string) net.Listener {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					fmt.Fprintf(c, "%s:%s\n", name, s.Text())
				}
			}()
		}
	}()
	return l
}
func freeGatewayAddress(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := l.Addr().String()
	l.Close()
	return address
}
func TestGatewayActualProcessesWithMajorityAuthorizationAndRoundTrip(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d-gateway", count), func(t *testing.T) { gatewayActualProcessCase(t, count) })
	}
}
func gatewayActualProcessCase(t *testing.T, count int) {
	binary := os.Getenv("CG_GATEWAY_BINARY")
	if binary == "" {
		t.Fatal("CG_GATEWAY_BINARY is required for this explicit integration suite")
	}
	local, cloud := tcpBackend(t, "local"), tcpBackend(t, "cloud")
	_, lp, _ := net.SplitHostPort(local.Addr().String())
	_, cp, _ := net.SplitHostPort(cloud.Addr().String())
	localPort, _ := strconv.Atoi(lp)
	cloudPort, _ := strconv.Atoi(cp)
	f := gatewayFixtureWithPorts(t, localPort, cloudPort)
	root, e := filepath.Abs("../../.build/gateway")
	if e != nil {
		t.Fatal(e)
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp(root, "process-integration-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	caPath := filepath.Join(dir, "ca.crt")
	os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ha-test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter}
	der, e = x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certPath := filepath.Join(dir, "controller.crt")
	keyPath := filepath.Join(dir, "controller.key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0600)
	urls := []string{}
	frontends := []string{}
	processes := []*exec.Cmd{}
	for index := 0; index < count; index++ {
		management, data, health := freeGatewayAddress(t), freeGatewayAddress(t), freeGatewayAddress(t)
		urls = append(urls, "https://"+management)
		frontends = append(frontends, data)
		config := map[string]interface{}{"management_listen": management, "health_listen": health, "allowed_controller_names": []string{"ha-test"}, "state_directory": filepath.Join(dir, fmt.Sprint(index)), "max_grant_ttl_seconds": 30, "tls": map[string]string{"cert_file": certPath, "key_file": keyPath, "ca_file": caPath}, "clusters": []interface{}{map[string]interface{}{"id": string(f.c.ResourceID), "listen": data, "backends": map[string]string{string(f.source.ResourceID): local.Addr().String(), string(f.target.ResourceID): cloud.Addr().String()}}}}
		encoded, _ := json.Marshal(config)
		path := filepath.Join(dir, fmt.Sprintf("gateway-%d.json", index))
		os.WriteFile(path, encoded, 0600)
		log, e := os.Create(filepath.Join(dir, fmt.Sprintf("gateway-%d.log", index)))
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(binary, "--config", path)
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		processes = append(processes, cmd)
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); log.Close() })
	}
	client, e := endpoint.NewGatewayHTTP(endpoint.GatewayProfile{URLs: urls, AllowSingleGateway: count == 1, CAFile: caPath, CertFile: certPath, KeyFile: keyPath})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, e = client.Statuses(context.Background(), f.c.ResourceID)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": client})
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	if e = keeper.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	roundTrip := func(c net.Conn, want string) {
		t.Helper()
		c.SetDeadline(time.Now().Add(time.Second))
		fmt.Fprintln(c, "write")
		got, e := bufio.NewReader(c).ReadString('\n')
		if e != nil || got != want+":write\n" {
			t.Fatalf("target=%q err=%v", got, e)
		}
	}
	old, e := net.Dial("tcp", frontends[0])
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	roundTrip(old, "local")
	resolved := f.resolved()
	auth, e := f.p.AuthorizeTransition(context.Background(), resolved)
	if e != nil {
		t.Fatal(e)
	}
	defer auth.Cancel()
	old.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = old.Read(make([]byte, 1)); e == nil {
		t.Fatal("old real TCP session survived withdrawal")
	}
	if e = f.p.Transfer(context.Background(), resolved); e != nil {
		t.Fatal(e)
	}
	if e = auth.Finalize(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, address := range frontends {
		c, e := net.Dial("tcp", address)
		if e != nil {
			t.Fatal(e)
		}
		roundTrip(c, "cloud")
		c.Close()
	}
	for n := range f.i.top.Instances {
		v := &f.i.top.Instances[n]
		v.Role = model.RoleReplica
		v.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true"}
		if v.ResourceID == f.target.ResourceID {
			v.Role = model.RolePrimary
			v.EngineMetadata = map[string]string{"read_only": "false", "super_read_only": "false"}
		}
	}
	before := f.i.GatewayGrant(f.c.ResourceID)
	if e = keeper.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	after := f.i.GatewayGrant(f.c.ResourceID)
	if after.Sequence <= before.Sequence || after.Epoch != before.Epoch {
		t.Fatal("actual stable renewal failed")
	}
	if count == 2 {
		processes[1].Process.Kill()
		processes[1].Wait()
		prior := f.i.GatewayGrant(f.c.ResourceID)
		for n := 0; n < 2; n++ {
			e = keeper.RunOnce(context.Background())
			if e == nil {
				t.Fatal("unavailable peer was not reported")
			}
			updated := f.i.GatewayGrant(f.c.ResourceID)
			if updated.Epoch != prior.Epoch || updated.Sequence <= prior.Sequence {
				t.Fatal("surviving real process lost stable renewal", e)
			}
			connection, dialErr := net.Dial("tcp", frontends[0])
			if dialErr != nil {
				t.Fatal(dialErr)
			}
			roundTrip(connection, "cloud")
			connection.Close()
			prior = updated
		}
		if _, e = f.p.AuthorizeTransition(context.Background(), f.resolved()); e == nil {
			t.Fatal("offline peer allowed transition")
		}
	}
	t.Log("PASS: real gateway processes, HA LeaseStore publisher, fixed TCP entry local->cloud, old-session close and OwnershipKeeper renewal. Mutation authority and SQL topology are fixtures; no real Raft cluster, database or cloud LB.")
}
