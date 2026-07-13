package agent

import (
	"context"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	outputs map[string]string
	calls   []string
}

func (runner *fakeCommandRunner) Run(_ context.Context, name string, arguments ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(arguments, " "))
	runner.calls = append(runner.calls, call)
	return []byte(runner.outputs[call]), nil
}

func TestVIPAcquireIsIdempotentAndSendsGARP(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{
		"/sbin/ip -4 -o addr show dev ens160": "2: ens160 inet 192.0.2.100/24 scope global ens160\n",
	}}
	controller := NewLinuxVIPController(runner, "/sbin/ip", "/usr/sbin/arping")
	policy := ClusterPolicy{VIP: "192.0.2.100", Interface: "ens160", Prefix: 24}
	if err := controller.Acquire(context.Background(), policy); err != nil {
		t.Fatalf("acquire VIP: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	if strings.Contains(joined, "addr add") {
		t.Fatalf("idempotent acquire added an existing VIP: %s", joined)
	}
	if !strings.Contains(joined, "/usr/sbin/arping -U -c 1 -I ens160 192.0.2.100") {
		t.Fatalf("idempotent acquire did not refresh ARP: %s", joined)
	}
}

func TestVIPAcquireAddsAddressBeforeGARP(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string]string{}}
	controller := NewLinuxVIPController(runner, "/sbin/ip", "/usr/sbin/arping")
	policy := ClusterPolicy{VIP: "192.0.2.100", Interface: "ens160", Prefix: 24}
	if err := controller.Acquire(context.Background(), policy); err != nil {
		t.Fatalf("acquire VIP: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	add := strings.Index(joined, "/sbin/ip addr add 192.0.2.100/24 dev ens160")
	arp := strings.Index(joined, "/usr/sbin/arping -U -c 1 -I ens160 192.0.2.100")
	if add < 0 || arp < 0 || add > arp {
		t.Fatalf("VIP command order=%s", joined)
	}
}
