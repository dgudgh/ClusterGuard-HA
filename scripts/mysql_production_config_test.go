package scripts

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cnfKey(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || !strings.Contains(line, "=") {
		return ""
	}
	key, _, _ := strings.Cut(line, "=")
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

func TestProductionMySQLOriginalsAndOnlyApprovedSubstitutions(t *testing.T) {
	allowed := map[string]bool{}
	for _, key := range strings.Fields("port server_id basedir datadir socket pid_file plugin_dir log_error general_log_file slow_query_log_file log_bin log_bin_index relay_log relay_log_index tmpdir read_only super_read_only") {
		allowed[key] = true
	}
	for name, digest := range map[string]string{
		"my-master.cnf": "100c5354517e774421cdb0ea713a607288d8d298ab0a1c8eb515d01ceaa91d1b",
		"my-slave.cnf":  "ae4bf82b60620165731d27744817980ae1bcd50865c4a1b008c4817039b4069c",
	} {
		t.Run(name, func(t *testing.T) {
			template, _ := filepath.Abs(filepath.Join("..", "configs", "mysql", name))
			original, err := os.ReadFile(template)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(original)); got != digest {
				t.Fatalf("user production original changed: %s", got)
			}
			for _, roots := range [][2]string{{"/data", ""}, {"/db-site", "/apps/mysql-8.0"}} {
				output := filepath.Join(t.TempDir(), "my.cnf")
				cmd := exec.Command("bash", "-c", `set -e; source ./clusterguard-mysql-layout.sh; cg_mysql_render_cnf "$1" "$2" "$3" "$4" 3307 1523307`, "render", template, output, roots[0], roots[1])
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("render: %v: %s", err, b)
				}
				got, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
				for i, line := range strings.Split(strings.TrimSuffix(string(original), "\n"), "\n") {
					if !allowed[cnfKey(line)] && (i >= len(lines) || lines[i] != line) {
						t.Fatalf("production parameter/comment changed at line %d: %q", i+1, line)
					}
				}
				software := roots[1]
				if software == "" {
					software = "/usr/local/mysql"
				}
				for _, want := range []string{"basedir=" + software, "plugin_dir=" + software + "/lib/plugin/", "datadir=" + roots[0] + "/mysql/3307/data", "server_id=1523307", "read_only=ON", "super_read_only=ON", roots[0] + "/mysql/3307/binlog/mysql-bin", roots[0] + "/mysql/3307/run/mysql.sock", "max_connections = 800", "join_buffer_size = 128M", "innodb_buffer_pool_size = 500M"} {
					if !strings.Contains(string(got), want) {
						t.Fatalf("missing preserved/rendered value %q", want)
					}
				}
				if strings.Contains(string(got), "=/mysql/") {
					t.Fatal("active path still points at production template host")
				}
			}
		})
	}
}

func TestMySQLLayoutAndIPPortIdentity(t *testing.T) {
	cmd := exec.Command("bash", "-c", `set -e; source ./clusterguard-mysql-layout.sh
for ip in 192.168.102.152 192.168.102.153 192.168.102.154; do cg_mysql_server_id "$ip" 3306; done
cg_mysql_layout /data 3306 ''; printf '%s\n' "$cg_mysql_software" "$cg_mysql_data"
cg_mysql_layout /db 3307 /apps/mysql8; printf '%s\n' "$cg_mysql_software" "$cg_mysql_conf"
`)
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("layout: %v: %s", err, got)
	}
	want := "1523306\n1533306\n1543306\n/usr/local/mysql\n/data/mysql/3306/data\n/apps/mysql8\n/db/mysql/3307/conf\n"
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	for _, pair := range [][2]string{{"/", ""}, {"/data/../root", ""}, {"/data", "/data"}, {"/data", "/data/mysql/3306/data"}, {"/data", "/data/mysql/3306/conf/bin"}, {"/data", "/bad path"}} {
		cmd := exec.Command("bash", "-c", `source ./clusterguard-mysql-layout.sh; cg_mysql_layout "$1" 3306 "$2"`, "invalid", pair[0], pair[1])
		if b, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("unsafe layout accepted %v: %s", pair, b)
		}
	}
}

func TestMySQLProductionBranchDoesNotRunAutomaticTuning(t *testing.T) {
	b, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "# Production MySQL 8.0")
	if start < 0 {
		t.Fatal("missing production branch")
	}
	end := strings.Index(s[start:], "authentication_options=")
	if start < 0 || end < 0 {
		t.Fatal("missing production branch")
	}
	cmd := exec.Command("bash", "-c", "set -e\nrelease_family=8.0\ndetect_total_memory_mb() { exit 91; }\ncalculate_memory_tuning() { exit 92; }\n"+s[start:start+end])
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("production cnf was subjected to auto tuning: %v: %s", err, b)
	}
}

func TestNativeProductionConfigGenerationHasNoTuningDependency(t *testing.T) {
	script, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	start := strings.Index(s, `mysql_version="$(${mysqld} --version`)
	end := strings.Index(s, "# Initialization uses a disposable option file")
	if start < 0 || end <= start {
		t.Fatal("cannot locate actual configuration generation")
	}
	for _, name := range []string{"my-master.cnf", "my-slave.cnf"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			original, err := os.ReadFile(filepath.Join("..", "configs", "mysql", name))
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"target": map[string]any{"mysql_cnf": string(original)}})
			writeFile(t, filepath.Join(root, "payload.json"), string(payload), 0600)
			writeExecutable(t, filepath.Join(root, "apps/mysql/bin/mysqld"), "#!/usr/bin/env bash\nif [[ $1 == --version ]]; then printf 'mysqld Ver 8.0.44 for Linux on x86_64\n'; else printf validated >\"$CG_TEST_ROOT/validated\"; fi\n")
			cmd := exec.Command("bash", "-c", `set -euo pipefail
source ./clusterguard-mysql-layout.sh
data_root="$1/site"; software_dir="$1/apps/mysql"; port=3306; server_id=1523306
cg_mysql_layout "$data_root" "$port" "$software_dir"
install_parent="$cg_mysql_instance"; config_dir="$cg_mysql_conf"; config_file="$config_dir/my.cnf"
log_dir="$cg_mysql_log"; run_dir="$cg_mysql_run"; data_dir="$cg_mysql_data"; socket="$run_dir/mysql.sock"
payload="$1/payload.json"; mysqld="$software_dir/bin/mysqld"; production_template=''
root_password=''; discovery_password=''; operation_password=''; replication_password=''
mkdir -p "$config_dir" "$log_dir" "$run_dir" "$data_dir" "$install_parent/binlog" "$install_parent/relaylog" "$install_parent/tmp"
chown() { :; }
`+s[start:end]+`
rm -f "$production_template"
`, "native-generation", root)
			cmd.Env = append(os.Environ(), "CG_TEST_ROOT="+root)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("actual generation failed: %v: %s", err, b)
			}
			if _, err := os.Stat(filepath.Join(root, "validated")); err != nil {
				t.Fatal("native validator was never called")
			}
			b, err := os.ReadFile(filepath.Join(root, "site/mysql/3306/conf/my.cnf"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "max_connections = 800") || !strings.Contains(string(b), "innodb_buffer_pool_size = 500M") {
				t.Fatal("actual installation retuned the approved configuration")
			}
		})
	}
}

func TestOfflinePayloadAndAgentShareCustomMySQLPaths(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("bash", "-c", `set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
source ./clusterguard-mysql-layout.sh
database_engine=mysql; database_port=3306; database_data_root=/db-site; database_software_dir=/apps/mysql8
mysql_master_cnf=../configs/mysql/my-master.cnf; mysql_slave_cnf=../configs/mysql/my-slave.cnf
data_nodes=(192.0.2.152 192.0.2.153); database_package=/tmp/mysql.tar.gz; jq_binary=jq; secrets_file=/unused
work_dir="$1"; mkdir -p "$work_dir/nodes/192.0.2.152"
node_id_for() { printf '11111111-1111-4111-8111-111111111111'; }
node_name_for() { printf 'cg-node-0001'; }
node_hostname_for() { printf 'db-node'; }
instance_id_for() { printf '22222222-2222-4222-8222-222222222222'; }
state_cluster_id() { printf '33333333-3333-4333-8333-333333333333'; }
controller_urls_json() { printf '[]'; }
read_env_value() { printf 'fixture-value'; }
mysql_payload 192.0.2.152 >"$work_dir/payload.json"
generate_agent_config 192.0.2.152
`, "offline-config", root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline config: %v: %s", err, b)
	}
	var payload struct {
		Target struct {
			DataRoot    string `json:"data_root"`
			SoftwareDir string `json:"software_dir"`
			ServerID    int    `json:"server_id"`
			Cnf         string `json:"mysql_cnf"`
		} `json:"target"`
	}
	b, _ := os.ReadFile(filepath.Join(root, "payload.json"))
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Target.DataRoot != "/db-site" || payload.Target.SoftwareDir != "/apps/mysql8" || payload.Target.ServerID != 1523306 || !strings.Contains(payload.Target.Cnf, "WRITESET_SESSION") {
		t.Fatalf("incorrect production payload: %+v", payload.Target)
	}
	var agent struct {
		Clusters []struct {
			Binary         string `json:"mysql_binary"`
			Defaults       string `json:"mysql_defaults_file"`
			Server         string `json:"mysql_server_binary"`
			ServerDefaults string `json:"mysql_server_defaults_file"`
		} `json:"clusters"`
	}
	b, _ = os.ReadFile(filepath.Join(root, "nodes/192.0.2.152/agent.json"))
	if err := json.Unmarshal(b, &agent); err != nil {
		t.Fatal(err)
	}
	if len(agent.Clusters) != 1 {
		t.Fatal("missing agent cluster")
	}
	p := agent.Clusters[0]
	if p.Binary != "/apps/mysql8/bin/mysql" || p.Server != "/apps/mysql8/bin/mysqld" || p.Defaults != "/db-site/mysql/3306/conf/client.cnf" || p.ServerDefaults != "/db-site/mysql/3306/conf/my.cnf" {
		t.Fatalf("agent lost custom paths: %+v", p)
	}
}

func TestMySQLProductionPathsAppearInOfflinePlan(t *testing.T) {
	root := t.TempDir()
	rpm := filepath.Join(root, "application.rpm")
	writeFile(t, rpm, "fixture", 0644)
	archive := fakeDatabaseArchive(t, "mysql", "")
	defaultArgs := []string{"-l", "192.0.2.152,192.0.2.153,192.0.2.154", "--data-on-arbitrators", "-g", rpm, "-r", archive, "--plan"}
	defaultPlan, err := runMultiNodeInstallerPlan(t, defaultArgs...)
	if err != nil {
		t.Fatalf("default plan: %v: %s", err, defaultPlan)
	}
	for _, want := range []string{"/usr/local/mysql", "/data/mysql/3306/data"} {
		if !strings.Contains(defaultPlan, want) {
			t.Fatalf("default plan omitted %q: %s", want, defaultPlan)
		}
	}
	args := []string{"-l", "192.0.2.152,192.0.2.153,192.0.2.154", "--data-on-arbitrators", "-g", rpm, "-r", archive, "-nd", "/db", "--database-software-dir", "/apps/mysql8", "--plan"}
	got, err := runMultiNodeInstallerPlan(t, args...)
	if err != nil {
		t.Fatalf("plan: %v: %s", err, got)
	}
	for _, want := range []string{"/apps/mysql8", "/db/mysql/3306/data", "192.0.2.152 -> 1523306", "my-master.cnf", "my-slave.cnf", "只读计划完成"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plan omitted %q: %s", want, got)
		}
	}
	args[1] = "192.0.2.152,198.51.100.152,192.0.2.154"
	if got, err := runMultiNodeInstallerPlan(t, args...); err == nil || !strings.Contains(got, "server_id 重复") {
		t.Fatalf("duplicate identity not rejected: %v: %s", err, got)
	}
}
