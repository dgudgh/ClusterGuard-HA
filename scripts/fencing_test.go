package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type vmwareFenceResponse struct {
	Status string `json:"status"`
	Fenced bool   `json:"fenced"`
}

func runVMwareFencer(t *testing.T, state, address, action string) (vmwareFenceResponse, error) {
	t.Helper()
	directory := t.TempDir()
	identity := filepath.Join(directory, "identity")
	knownHosts := filepath.Join(directory, "known_hosts")
	targets := filepath.Join(directory, "targets.tsv")
	configuration := filepath.Join(directory, "vmware.conf")
	mockSSH := filepath.Join(directory, "ssh")
	for path, contents := range map[string]string{
		identity:   "test identity\n",
		knownHosts: "vmware-host ssh-ed25519 AAAATEST\n",
		targets:    "192.0.2.10|G:\\mysql\\mysql01\\mysql01.vmx\n",
		mockSSH:    "#!/usr/bin/env bash\nprintf '%s\\n' \"${CG_FENCER_MOCK_STATE:-FENCED}\"\n",
	} {
		mode := os.FileMode(0o600)
		if path == mockSSH {
			mode = 0o700
		}
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
	}
	configContents := strings.Join([]string{
		"vmware_host=vmware-host",
		"vmware_user=clusterguard-fencer",
		"vmrun_path=D:\\vmware\\vmrun.exe",
		"ssh_port=22",
		"connect_timeout_seconds=5",
		"identity_file=" + identity,
		"known_hosts_file=" + knownHosts,
		"targets_file=" + targets,
		"",
	}, "\n")
	if err := os.WriteFile(configuration, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required for the shell fencer test")
	}
	command := exec.Command("bash", filepath.Join("fencing", "vmware-workstation-ssh-fencer"), action)
	command.Env = append(os.Environ(),
		"CG_FENCER_CONFIG="+configuration,
		"CG_FENCER_JQ_BINARY="+jq,
		"CG_FENCER_SSH_BINARY="+mockSSH,
		"CG_FENCER_MOCK_STATE="+state,
	)
	command.Stdin = strings.NewReader(`{"cluster_id":"cluster-id","operation_id":"operation-id","instance":{"ip_address":"` + address + `"}}`)
	output, runErr := command.CombinedOutput()
	response := vmwareFenceResponse{}
	if runErr == nil {
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatalf("decode fencer response %q: %v", output, err)
		}
	}
	return response, runErr
}

func TestVMwareWorkstationFencerReportsProviderState(t *testing.T) {
	for _, test := range []struct {
		name   string
		state  string
		action string
		fenced bool
	}{
		{name: "running status", state: "RUNNING", action: "status", fenced: false},
		{name: "fenced status", state: "FENCED", action: "status", fenced: true},
		{name: "fence confirmation", state: "FENCED", action: "fence", fenced: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := runVMwareFencer(t, test.state, "192.0.2.10", test.action)
			if err != nil || response.Status != "ok" || response.Fenced != test.fenced {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestVMwareWorkstationFencerRejectsUnmappedTargets(t *testing.T) {
	if _, err := runVMwareFencer(t, "FENCED", "192.0.2.99", "status"); err == nil {
		t.Fatal("unmapped database instance was accepted")
	}
}
