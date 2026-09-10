package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
	"clusterguard.io/ha/pkg/redact"
)

const maximumAPIResponseBytes = 8 << 20

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newAPIClient(caFile string) (*http.Client, error) {
	contents, err := os.ReadFile(strings.TrimSpace(caFile))
	if err != nil {
		return nil, fmt.Errorf("read control-plane CA: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(contents) {
		return nil, fmt.Errorf("control-plane CA contains no valid certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}, nil
}

type apiEnvelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

type approvalIssueRequest struct {
	ClusterID      model.ResourceID    `json:"cluster_id"`
	Engine         model.Engine        `json:"engine"`
	OperationKind  model.OperationKind `json:"operation_kind"`
	TargetID       model.ResourceID    `json:"target_id"`
	IssuedBy       string              `json:"issued_by"`
	TTLSeconds     int64               `json:"ttl_seconds"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
}

func requestFor(arguments []string) (method string, path string, err error) {
	if len(arguments) == 0 {
		return "", "", fmt.Errorf("command is required: status, engines, clusters, topology, health, candidates, metrics, refresh, operation, approval")
	}
	command := arguments[0]
	switch command {
	case "version", "status", "engines", "clusters":
		if len(arguments) != 1 {
			return "", "", fmt.Errorf("%s does not accept arguments", command)
		}
		if command == "version" {
			return http.MethodGet, "/api/v1/platform/version", nil
		}
		if command == "status" {
			return http.MethodGet, "/api/v1/control-plane/status", nil
		}
		return http.MethodGet, "/api/v1/" + command, nil
	case "topology", "health", "candidates", "metrics", "refresh":
		if len(arguments) != 2 || strings.TrimSpace(arguments[1]) == "" {
			return "", "", fmt.Errorf("%s requires a platform cluster UUID", command)
		}
		method := http.MethodGet
		action := command
		if command == "refresh" {
			method = http.MethodPost
			action = "discover"
		}
		return method, "/api/v1/clusters/" + url.PathEscape(arguments[1]) + "/" + action, nil
	case "operation":
		if len(arguments) != 2 || strings.TrimSpace(arguments[1]) == "" {
			return "", "", fmt.Errorf("operation requires a platform operation UUID")
		}
		return http.MethodGet, "/api/v1/operations/" + url.PathEscape(arguments[1]), nil
	case "approval":
		if len(arguments) < 2 {
			return "", "", fmt.Errorf("approval requires a subcommand: issue, list, show")
		}
		switch arguments[1] {
		case "issue":
			return http.MethodPost, "/api/v1/approvals", nil
		case "list":
			if len(arguments) != 2 {
				return "", "", fmt.Errorf("approval list does not accept arguments")
			}
			return http.MethodGet, "/api/v1/approvals", nil
		case "show":
			if len(arguments) != 3 || strings.TrimSpace(arguments[2]) == "" {
				return "", "", fmt.Errorf("approval show requires a platform approval UUID")
			}
			return http.MethodGet, "/api/v1/approvals/" + url.PathEscape(arguments[2]), nil
		default:
			return "", "", fmt.Errorf("unknown approval subcommand %q", arguments[1])
		}
	default:
		return "", "", fmt.Errorf("unknown command %q", command)
	}
}

func parseApprovalIssue(arguments []string, stderr io.Writer) (approvalIssueRequest, error) {
	flags := flag.NewFlagSet("cgctl approval issue", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterID := flags.String("cluster", "", "platform cluster UUID")
	engine := flags.String("engine", "mysql", "database engine")
	kind := flags.String("kind", "", "operation kind")
	targetID := flags.String("target", "", "platform target instance UUID")
	issuedBy := flags.String("issued-by", "", "administrator identity")
	ttl := flags.Duration("ttl", 5*time.Minute, "single-use grant lifetime")
	idempotencyKey := flags.String("idempotency-key", "", "optional operation idempotency key")
	if err := flags.Parse(arguments); err != nil {
		return approvalIssueRequest{}, err
	}
	if flags.NArg() != 0 {
		return approvalIssueRequest{}, fmt.Errorf("approval issue does not accept positional arguments")
	}
	request := approvalIssueRequest{
		ClusterID:      model.ResourceID(strings.TrimSpace(*clusterID)),
		Engine:         model.Engine(strings.TrimSpace(*engine)),
		OperationKind:  model.OperationKind(strings.TrimSpace(*kind)),
		TargetID:       model.ResourceID(strings.TrimSpace(*targetID)),
		IssuedBy:       strings.TrimSpace(*issuedBy),
		TTLSeconds:     int64((*ttl) / time.Second),
		IdempotencyKey: strings.TrimSpace(*idempotencyKey),
	}
	if !model.ValidResourceID(request.ClusterID) || !model.ValidResourceID(request.TargetID) ||
		!request.Engine.Valid() || request.OperationKind == "" || request.IssuedBy == "" {
		return approvalIssueRequest{}, fmt.Errorf("approval issue requires valid --cluster, --engine, --kind, --target, and --issued-by values")
	}
	if *ttl <= 0 || *ttl > 15*time.Minute || time.Duration(request.TTLSeconds)*time.Second != *ttl {
		return approvalIssueRequest{}, fmt.Errorf("approval TTL must be a whole number of seconds between 1s and 15m")
	}
	return request, nil
}

func run(arguments []string, stdout io.Writer, stderr io.Writer, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	serverURL := flags.String("server", "http://127.0.0.1:8088", "ClusterGuard HA API URL")
	jsonOutput := flags.Bool("json", false, "print the raw API response as indented JSON")
	controlTokenEnv := flags.String("token-env", "CG_CONTROL_TOKEN", "environment variable containing the control API token")
	caFile := flags.String("ca-file", strings.TrimSpace(os.Getenv("CG_TLS_CA_FILE")), "private CA used to authenticate the HTTPS control plane")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if strings.TrimSpace(*caFile) != "" {
		configuredClient, err := newAPIClient(*caFile)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cgctl:", err)
			return 2
		}
		client = configuredClient
	}
	arguments = flags.Args()
	if len(arguments) > 0 && arguments[0] == "cluster" {
		return runCluster(arguments[1:], stdout, stderr, *serverURL, *controlTokenEnv, client)
	}
	if len(arguments) > 0 && arguments[0] == "power" {
		return runPower(arguments[1:], stdout, stderr, *serverURL, *controlTokenEnv, *jsonOutput, client)
	}
	method, path, err := requestFor(arguments)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}

	environment := strings.TrimSpace(*controlTokenEnv)
	controlToken := ""
	if environment != "" {
		controlToken = strings.TrimSpace(os.Getenv(environment))
	}
	var requestBody io.Reader
	if method == http.MethodPost {
		if environment == "" {
			_, _ = fmt.Fprintln(stderr, "cgctl: control token environment variable name is required")
			return 2
		}
		if controlToken == "" {
			_, _ = fmt.Fprintf(stderr, "cgctl: control token environment variable %s is empty\n", environment)
			return 2
		}
		body := []byte("{}")
		if len(flags.Args()) >= 2 && flags.Args()[0] == "approval" && flags.Args()[1] == "issue" {
			issue, parseErr := parseApprovalIssue(flags.Args()[2:], stderr)
			if parseErr != nil {
				_, _ = fmt.Fprintln(stderr, "cgctl:", parseErr)
				return 2
			}
			body, err = json.Marshal(issue)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, "cgctl: invalid approval request")
				return 2
			}
		}
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, strings.TrimRight(*serverURL, "/")+path, requestBody)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid server URL")
		return 2
	}
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if controlToken != "" {
		request.Header.Set("Authorization", "Bearer "+controlToken)
	}
	response, err := client.Do(request)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: API request failed")
		return 1
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumAPIResponseBytes+1))
	if err != nil || len(raw) > maximumAPIResponseBytes {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API response")
		return 1
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API response")
		return 1
	}
	if response.StatusCode >= http.StatusBadRequest || envelope.Status == "error" {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = response.Status
		}
		_, _ = fmt.Fprintln(stderr, "cgctl:", redact.Text(message, controlToken))
		return 1
	}
	issueApproval := len(flags.Args()) >= 2 && flags.Args()[0] == "approval" && flags.Args()[1] == "issue"
	if !issueApproval {
		raw, err = redact.JSON(raw)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cgctl: invalid diagnostic response")
			return 1
		}
		if err = json.Unmarshal(raw, &envelope); err != nil {
			return 1
		}
	}
	if *jsonOutput {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, raw, "", "  "); err != nil {
			_, _ = fmt.Fprintln(stderr, "cgctl: invalid API response")
			return 1
		}
		formatted.WriteByte('\n')
		_, _ = formatted.WriteTo(stdout)
		return 0
	}
	humanCommand := flags.Args()[0]
	if humanCommand == "approval" && len(flags.Args()) >= 2 {
		humanCommand += " " + flags.Args()[1]
	}
	if err := writeHuman(stdout, humanCommand, envelope.Result); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API result")
		return 1
	}
	return 0
}

func writeHuman(writer io.Writer, command string, result json.RawMessage) error {
	switch command {
	case "version":
		var version struct {
			Product         string `json:"product"`
			Binary          string `json:"binary"`
			Version         string `json:"version"`
			Release         string `json:"release"`
			Commit          string `json:"commit"`
			BuiltAt         string `json:"built_at"`
			RPMArchitecture string `json:"rpm_architecture"`
			StateFormat     int    `json:"state_format"`
			UpdateProtocol  int    `json:"update_protocol"`
		}
		if err := json.Unmarshal(result, &version); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(writer, "%s\tbinary=%s\tversion=%s-%s\tarch=%s\n",
			valueOrUnknown(version.Product), valueOrUnknown(version.Binary),
			valueOrUnknown(version.Version), valueOrUnknown(version.Release), valueOrUnknown(version.RPMArchitecture))
		_, _ = fmt.Fprintf(writer, "state_format=%d\tupdate_protocol=%d\tcommit=%s\tbuilt_at=%s\n",
			version.StateFormat, version.UpdateProtocol, valueOrUnknown(version.Commit), valueOrUnknown(version.BuiltAt))
	case "status":
		var status struct {
			Mode                    string           `json:"mode"`
			LocalControllerID       model.ResourceID `json:"local_controller_id"`
			Role                    string           `json:"role"`
			LeaderID                model.ResourceID `json:"leader_id"`
			LeaderKnown             bool             `json:"leader_known"`
			VoterCount              int              `json:"voter_count"`
			QuorumConfirmed         bool             `json:"quorum_confirmed"`
			MutationAuthority       bool             `json:"mutation_authority"`
			SnapshotCASActive       bool             `json:"snapshot_cas_active"`
			StateRevision           uint64           `json:"state_revision"`
			Ready                   bool             `json:"ready"`
			ReadinessReason         string           `json:"readiness_reason"`
			UptimeSeconds           int64            `json:"uptime_seconds"`
			ClusterCount            int              `json:"cluster_count"`
			ActiveOperations        int              `json:"active_operations"`
			IndeterminateOperations int              `json:"indeterminate_operations"`
			ActiveLifecycleTasks    int              `json:"active_lifecycle_tasks"`
		}
		if err := json.Unmarshal(result, &status); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(writer, "ready=%s\treason=%s\tmode=%s\trole=%s\tquorum=%s\tmutation_authority=%s\tsnapshot_cas=%s\n",
			yesNo(status.Ready), valueOrUnknown(status.ReadinessReason), valueOrUnknown(status.Mode), valueOrUnknown(status.Role),
			yesNo(status.QuorumConfirmed), yesNo(status.MutationAuthority), yesNo(status.SnapshotCASActive))
		_, _ = fmt.Fprintf(writer, "local=%s\tleader=%s\tleader_known=%s\tvoters=%d\trevision=%d\tuptime=%ds\n",
			valueOrDash(string(status.LocalControllerID)), valueOrDash(string(status.LeaderID)), yesNo(status.LeaderKnown), status.VoterCount, status.StateRevision, status.UptimeSeconds)
		_, _ = fmt.Fprintf(writer, "clusters=%d\tactive_operations=%d\tindeterminate_operations=%d\tactive_lifecycle_tasks=%d\n",
			status.ClusterCount, status.ActiveOperations, status.IndeterminateOperations, status.ActiveLifecycleTasks)
	case "engines":
		var engines []struct {
			Engine   model.Engine `json:"engine"`
			Features map[string]struct {
				Available bool `json:"available"`
			} `json:"features"`
		}
		if err := json.Unmarshal(result, &engines); err != nil {
			return err
		}
		for _, engine := range engines {
			features := make([]string, 0)
			for name, state := range engine.Features {
				if state.Available {
					features = append(features, name)
				}
			}
			sort.Strings(features)
			_, _ = fmt.Fprintf(writer, "%s\tfeatures=%s\n", engine.Engine, valueOrDash(strings.Join(features, ",")))
		}
	case "clusters":
		var clusters []model.DatabaseCluster
		if err := json.Unmarshal(result, &clusters); err != nil {
			return err
		}
		for _, cluster := range clusters {
			_, _ = fmt.Fprintf(writer, "%s\t%s\tengine=%s\thealth=%s\n", cluster.ResourceID, valueOrDash(cluster.DisplayName), cluster.Engine, valueOrUnknown(string(cluster.Health.State)))
		}
	case "topology", "refresh":
		var snapshot model.TopologySnapshot
		if err := json.Unmarshal(result, &snapshot); err != nil {
			return err
		}
		if command == "refresh" {
			_, _ = fmt.Fprintf(writer, "%s\trefreshed\thealth=%s\n", snapshot.ClusterID, valueOrUnknown(string(snapshot.Health.State)))
		}
		for _, instance := range snapshot.Instances {
			_, _ = fmt.Fprintf(writer, "%s\t%s\trole=%s\thealth=%s\tlag=%s\n",
				instance.ResourceID, displayEndpoint(instance), valueOrUnknown(string(instance.Role)),
				valueOrUnknown(string(instance.Health.State)), lagText(instance.Replication.LagSeconds))
		}
	case "health":
		var health struct {
			ClusterID model.ResourceID    `json:"cluster_id"`
			Health    model.Health        `json:"health"`
			Probes    []model.ProbeStatus `json:"probes"`
		}
		if err := json.Unmarshal(result, &health); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(writer, "%s\thealth=%s\n", health.ClusterID, valueOrUnknown(string(health.Health.State)))
		for _, probe := range health.Probes {
			identity := probe.InstanceID
			if identity == "" {
				identity = probe.EndpointID
			}
			_, _ = fmt.Fprintf(writer, "%s\thealth=%s\n", identity, valueOrUnknown(string(probe.Health.State)))
		}
	case "candidates":
		var candidates []model.CandidateAssessment
		if err := json.Unmarshal(result, &candidates); err != nil {
			return err
		}
		for _, candidate := range candidates {
			_, _ = fmt.Fprintf(writer, "%s\trank=%d\teligible=%s\trisk=%s\tdata_loss=%s\n",
				candidate.InstanceID, candidate.Rank, yesNo(candidate.Eligible), valueOrUnknown(candidate.RiskLevel), valueOrUnknown(candidate.DataLossRisk))
		}
	case "metrics":
		var metrics struct {
			Instances []struct {
				InstanceID model.ResourceID   `json:"instance_id"`
				Values     map[string]float64 `json:"values"`
			} `json:"instances"`
		}
		if err := json.Unmarshal(result, &metrics); err != nil {
			return err
		}
		for _, instance := range metrics.Instances {
			names := make([]string, 0, len(instance.Values))
			for name := range instance.Values {
				names = append(names, name)
			}
			sort.Strings(names)
			_, _ = fmt.Fprint(writer, instance.InstanceID)
			for _, name := range names {
				_, _ = fmt.Fprintf(writer, "\t%s=%g", name, instance.Values[name])
			}
			_, _ = fmt.Fprintln(writer)
		}
	case "operation":
		var operation model.OperationRecord
		if err := json.Unmarshal(result, &operation); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(writer, "%s\tengine=%s\tkind=%s\ttarget=%s\tstage=%s\tstatus=%s\tmessage=%s\n",
			operation.ResourceID, operation.Operation.Engine, operation.Operation.Kind, operation.TargetID,
			valueOrUnknown(string(operation.Stage)), valueOrUnknown(string(operation.Status)), valueOrDash(operation.Message))
	case "approval issue":
		var issued struct {
			Operation     model.OperationRecord `json:"operation"`
			Grant         model.ApprovalGrant   `json:"grant"`
			ApprovalToken string                `json:"approval_token"`
		}
		if err := json.Unmarshal(result, &issued); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(writer, "Approval token is shown once and cannot be recovered.")
		_, _ = fmt.Fprintf(writer, "operation=%s\tgrant=%s\tcluster=%s\tengine=%s\tkind=%s\ttarget=%s\texpires=%s\n",
			issued.Operation.ResourceID, issued.Grant.ResourceID, issued.Grant.ClusterID, issued.Grant.Engine,
			issued.Grant.OperationKind, issued.Grant.TargetID, issued.Grant.ExpiresAt.UTC().Format(time.RFC3339))
		_, _ = fmt.Fprintln(writer, issued.ApprovalToken)
	case "approval list":
		var grants []model.ApprovalGrant
		if err := json.Unmarshal(result, &grants); err != nil {
			return err
		}
		for _, grant := range grants {
			writeApprovalGrant(writer, grant)
		}
	case "approval show":
		var grant model.ApprovalGrant
		if err := json.Unmarshal(result, &grant); err != nil {
			return err
		}
		writeApprovalGrant(writer, grant)
	default:
		return fmt.Errorf("unsupported human output command %q", command)
	}
	return nil
}

func writeApprovalGrant(writer io.Writer, grant model.ApprovalGrant) {
	_, _ = fmt.Fprintf(writer, "%s\toperation=%s\tcluster=%s\tengine=%s\tkind=%s\ttarget=%s\tstatus=%s\texpires=%s",
		grant.ResourceID, grant.OperationID, grant.ClusterID, grant.Engine, grant.OperationKind, grant.TargetID,
		valueOrUnknown(string(grant.Status)), grant.ExpiresAt.UTC().Format(time.RFC3339))
	if grant.ConsumedByOperationID != "" {
		_, _ = fmt.Fprintf(writer, "\tconsumed_by=%s", grant.ConsumedByOperationID)
	}
	_, _ = fmt.Fprintln(writer)
}

func displayEndpoint(instance model.DatabaseInstance) string {
	hostname := strings.TrimSpace(instance.Hostname)
	ipAddress := strings.TrimSpace(instance.IPAddress)
	if hostname != "" && ipAddress != "" {
		return fmt.Sprintf("%s (%s:%d)", hostname, ipAddress, instance.Port)
	}
	address := hostname
	if address == "" {
		address = ipAddress
	}
	if address == "" {
		address = strings.TrimSpace(instance.DisplayName)
	}
	if instance.Port > 0 {
		return fmt.Sprintf("%s:%d", valueOrDash(address), instance.Port)
	}
	return valueOrDash(address)
}

func lagText(lag *int64) string {
	if lag == nil {
		return "unknown"
	}
	return fmt.Sprintf("%ds", *lag)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func main() {
	client := &http.Client{Timeout: 10 * time.Second}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, client))
}

// clusterSnapshotDocument is the recovery contract written by
// clusterguard-cluster-shutdown.sh and consumed by restore-status.
type clusterSnapshotDocument struct {
	ClusterID   string `json:"cluster_id"`
	RecoveredAt string `json:"recovered_at"`
	Cluster     struct {
		ClusterName string `json:"cluster_name"`
		Primary     struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"primary"`
		Replicas []struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"replicas"`
	} `json:"cluster"`
}

func runCluster(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, client httpDoer) int {
	if len(arguments) == 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: cluster requires a subcommand: shutdown, restore-status")
		return 2
	}
	switch arguments[0] {
	case "shutdown":
		return runClusterShutdown(arguments[1:], stdout, stderr, serverURL, tokenEnv, client)
	case "restore-status":
		return runClusterRestoreStatus(arguments[1:], stdout, stderr, serverURL, tokenEnv, client)
	default:
		_, _ = fmt.Fprintf(stderr, "cgctl: unknown cluster subcommand %q\n", arguments[0])
		return 2
	}
}

// runClusterShutdown is a compatibility alias for the durable power
// lifecycle. It intentionally does not execute the legacy local shell helper:
// every mutation must pass through the same control-plane gates as the Web UI.
func runClusterShutdown(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl cluster shutdown", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name to shut down")
	mode := flags.String("mode", "service", "shutdown mode: service or poweroff")
	dryRun := flags.Bool("dry-run", false, "run the control-plane precheck without changing anything")
	approvalToken := flags.String("approval-token", "", "one-time approval token for real execution")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: cluster shutdown does not accept positional arguments")
		return 2
	}
	name := strings.TrimSpace(*clusterName)
	if name == "" {
		_, _ = fmt.Fprintln(stderr, "cgctl: cluster shutdown requires --cluster <display name>")
		return 2
	}
	modeValue := strings.TrimSpace(*mode)
	if modeValue != "service" && modeValue != "poweroff" {
		_, _ = fmt.Fprintln(stderr, "cgctl: shutdown mode must be service or poweroff")
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, name)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	basePath := "/api/v1/clusters/" + url.PathEscape(string(clusterID)) + "/power/"
	precheck, err := powerAPIPost(client, serverURL, tokenEnv, basePath+"precheck", map[string]interface{}{"mode": modeValue})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	var precheckResult struct {
		BlockingReasons []string `json:"blocking_reasons"`
	}
	if err := json.Unmarshal(precheck.Result, &precheckResult); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid power precheck response")
		return 1
	}
	if *dryRun {
		_, _ = powerAPIPost(client, serverURL, tokenEnv, basePath+"cancel", map[string]interface{}{})
		return powerResponse(stdout, stderr, false, precheck, "precheck")
	}
	if len(precheckResult.BlockingReasons) > 0 {
		_, _ = powerAPIPost(client, serverURL, tokenEnv, basePath+"cancel", map[string]interface{}{})
		_, _ = fmt.Fprintln(stderr, "cgctl: shutdown blocked: "+strings.Join(precheckResult.BlockingReasons, "; "))
		return 1
	}
	token := strings.TrimSpace(*approvalToken)
	if token == "" {
		_, _ = powerAPIPost(client, serverURL, tokenEnv, basePath+"cancel", map[string]interface{}{})
		_, _ = fmt.Fprintln(stderr, "cgctl: cluster shutdown requires --approval-token for real execution")
		return 2
	}
	if _, err := powerAPIPost(client, serverURL, tokenEnv, basePath+"plan", map[string]interface{}{"mode": modeValue}); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	executed, err := powerAPIPost(client, serverURL, tokenEnv, basePath+"execute", map[string]interface{}{"approval_token": token})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, false, executed, "execute")
}

// cgctlAPIGet performs a read-only API request with the configured control
// credential and returns the decoded envelope.
func cgctlAPIGet(client httpDoer, serverURL string, tokenEnv string, path string) (apiEnvelope, error) {
	var envelope apiEnvelope
	environment := strings.TrimSpace(tokenEnv)
	controlToken := ""
	if environment != "" {
		controlToken = strings.TrimSpace(os.Getenv(environment))
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+path, nil)
	if err != nil {
		return envelope, fmt.Errorf("invalid server URL")
	}
	request.Header.Set("Accept", "application/json")
	if controlToken != "" {
		request.Header.Set("Authorization", "Bearer "+controlToken)
	}
	response, err := client.Do(request)
	if err != nil {
		return envelope, fmt.Errorf("API request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumAPIResponseBytes+1))
	if err != nil || len(raw) > maximumAPIResponseBytes {
		return envelope, fmt.Errorf("invalid API response")
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return envelope, fmt.Errorf("invalid API response")
	}
	if response.StatusCode >= http.StatusBadRequest || envelope.Status == "error" {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = response.Status
		}
		return envelope, fmt.Errorf("%s", message)
	}
	return envelope, nil
}

func runClusterRestoreStatus(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl cluster restore-status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterID := flags.String("cluster", "", "platform cluster UUID (defaults to the local snapshot)")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: cluster restore-status does not accept positional arguments")
		return 2
	}
	snapshotPath := strings.TrimSpace(os.Getenv("CLUSTER_SNAPSHOT_PATH"))
	if snapshotPath == "" {
		snapshotPath = "/etc/clusterguard/cluster-topology.json"
	}
	var document clusterSnapshotDocument
	snapshotExists := false
	if contents, err := os.ReadFile(snapshotPath); err == nil {
		if jsonErr := json.Unmarshal(contents, &document); jsonErr == nil && document.ClusterID != "" {
			snapshotExists = true
		}
	}
	_, _ = fmt.Fprintf(stdout, "snapshot\t%s\t%s\n", snapshotPath, map[bool]string{true: "present", false: "absent"}[snapshotExists])
	if snapshotExists {
		_, _ = fmt.Fprintf(stdout, "cluster\t%s\tdisplay=%s\trecovered_at=%s\n",
			valueOrDash(document.ClusterID), valueOrDash(document.Cluster.ClusterName), valueOrDash(document.RecoveredAt))
	} else {
		_, _ = fmt.Fprintln(stdout, "snapshot_missing\tno planned shutdown recorded on this node")
	}

	targetID := strings.TrimSpace(*clusterID)
	if targetID == "" {
		targetID = document.ClusterID
	}
	if targetID == "" {
		_, _ = fmt.Fprintln(stdout, "cluster\t-\tno cluster UUID available (pass --cluster or create a snapshot first)")
		return 0
	}
	if !model.ValidResourceID(model.ResourceID(targetID)) {
		_, _ = fmt.Fprintf(stderr, "cgctl: invalid cluster UUID: %s\n", targetID)
		return 2
	}

	detail, err := cgctlAPIGet(client, serverURL, tokenEnv, "/api/v1/clusters/"+url.PathEscape(targetID))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	var detailView struct {
		Cluster struct {
			RecoveryFreeze bool   `json:"recovery_freeze"`
			DisplayName    string `json:"display_name"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(detail.Result, &detailView); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API result")
		return 1
	}
	topology, err := cgctlAPIGet(client, serverURL, tokenEnv, "/api/v1/clusters/"+url.PathEscape(targetID)+"/topology")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	var snapshot model.TopologySnapshot
	if err := json.Unmarshal(topology.Result, &snapshot); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API result")
		return 1
	}

	freezeState := "active"
	if detailView.Cluster.RecoveryFreeze {
		freezeState = "frozen"
	}
	_, _ = fmt.Fprintf(stdout, "recovery_freeze\t%s\n", freezeState)
	primaryHealthy := false
	for _, instance := range snapshot.Instances {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\trole=%s\thealth=%s\tlag=%s\tmaintenance=%s\n",
			instance.ResourceID, displayEndpoint(instance), valueOrUnknown(string(instance.Role)),
			valueOrUnknown(string(instance.Health.State)), lagText(instance.Replication.LagSeconds), yesNo(instance.Maintenance))
		if instance.Role == model.RolePrimary && instance.Health.State == model.HealthHealthy {
			primaryHealthy = true
		}
	}
	var advice string
	switch {
	case !snapshotExists:
		advice = "no planned shutdown snapshot on this node"
	case document.RecoveredAt != "":
		advice = "snapshot already finalized — planned-shutdown protection was released"
	case detailView.Cluster.RecoveryFreeze && primaryHealthy:
		advice = "cluster healthy but recovery freeze still active — clusterguard-cluster-finalize will release it"
	case detailView.Cluster.RecoveryFreeze && !primaryHealthy:
		advice = "primary offline — protection stays active (fail-closed); fix the primary, then restore/finalize resume"
	default:
		advice = "no recovery freeze recorded; verify instance health per line above"
	}
	_, _ = fmt.Fprintf(stdout, "advice\t%s\n", advice)
	return 0
}

// runPower drives the power lifecycle API from the CLI. Every subcommand
// resolves --cluster as a display name or UUID against the control plane.
func runPower(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer) int {
	if len(arguments) == 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: power requires a subcommand: precheck, plan, execute, cancel, boot-detected, recovering, verify, complete, fail, status")
		return 2
	}
	switch arguments[0] {
	case "precheck", "plan":
		return runPowerModeAction(arguments[1:], stdout, stderr, serverURL, tokenEnv, jsonOutput, client, arguments[0])
	case "execute":
		return runPowerExecute(arguments[1:], stdout, stderr, serverURL, tokenEnv, jsonOutput, client)
	case "cancel", "boot-detected", "recovering", "verify", "complete":
		return runPowerSimpleAction(arguments[1:], stdout, stderr, serverURL, tokenEnv, jsonOutput, client, arguments[0])
	case "fail":
		return runPowerFail(arguments[1:], stdout, stderr, serverURL, tokenEnv, jsonOutput, client)
	case "status":
		return runPowerStatus(arguments[1:], stdout, stderr, serverURL, tokenEnv, jsonOutput, client)
	default:
		_, _ = fmt.Fprintf(stderr, "cgctl: unknown power subcommand %q\n", arguments[0])
		return 2
	}
}

// powerClusterUUID accepts a platform UUID directly, or resolves a display
// name against the cluster inventory.
func powerClusterUUID(client httpDoer, serverURL string, tokenEnv string, nameOrID string) (model.ResourceID, error) {
	value := strings.TrimSpace(nameOrID)
	if value == "" {
		return "", fmt.Errorf("--cluster is required")
	}
	if model.ValidResourceID(model.ResourceID(value)) {
		return model.ResourceID(value), nil
	}
	envelope, err := cgctlAPIGet(client, serverURL, tokenEnv, "/api/v1/clusters")
	if err != nil {
		return "", err
	}
	var clusters []model.DatabaseCluster
	if err := json.Unmarshal(envelope.Result, &clusters); err != nil {
		return "", fmt.Errorf("invalid API result")
	}
	for _, cluster := range clusters {
		if strings.EqualFold(cluster.DisplayName, value) {
			return cluster.ResourceID, nil
		}
	}
	return "", fmt.Errorf("no cluster named %q", value)
}

// powerAPIPost sends a control-plane mutation with the administrative token
// and decodes the envelope.
func powerAPIPost(client httpDoer, serverURL string, tokenEnv string, path string, body map[string]interface{}) (apiEnvelope, error) {
	var envelope apiEnvelope
	environment := strings.TrimSpace(tokenEnv)
	if environment == "" {
		return envelope, fmt.Errorf("control token environment variable name is required")
	}
	controlToken := strings.TrimSpace(os.Getenv(environment))
	if controlToken == "" {
		return envelope, fmt.Errorf("control token environment variable %s is empty", environment)
	}
	contents, err := json.Marshal(body)
	if err != nil {
		return envelope, fmt.Errorf("invalid request body")
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(serverURL, "/")+path, bytes.NewReader(contents))
	if err != nil {
		return envelope, fmt.Errorf("invalid server URL")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+controlToken)
	response, err := client.Do(request)
	if err != nil {
		return envelope, fmt.Errorf("API request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximumAPIResponseBytes+1))
	if err != nil || len(raw) > maximumAPIResponseBytes {
		return envelope, fmt.Errorf("invalid API response")
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return envelope, fmt.Errorf("invalid API response")
	}
	if response.StatusCode >= http.StatusBadRequest || envelope.Status == "error" {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = response.Status
		}
		return envelope, fmt.Errorf("%s", message)
	}
	return envelope, nil
}

// powerResponse renders the envelope as indented JSON (--json) or the
// tab-separated human view.
func powerResponse(stdout io.Writer, stderr io.Writer, jsonOutput bool, envelope apiEnvelope, action string) int {
	var err error
	envelope.Result, err = redact.JSON(envelope.Result)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid diagnostic response")
		return 1
	}
	if jsonOutput {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, envelope.Result, "", "  "); err != nil {
			_, _ = fmt.Fprintln(stderr, "cgctl: invalid API response")
			return 1
		}
		formatted.WriteByte('\n')
		_, _ = formatted.WriteTo(stdout)
		return 0
	}
	if err := writePowerHuman(stdout, action, envelope.Result); err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl: invalid API result")
		return 1
	}
	return 0
}

func runPowerModeAction(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer, action string) int {
	flags := flag.NewFlagSet("cgctl power "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name or UUID")
	mode := flags.String("mode", "service", "shutdown mode: service or poweroff")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "cgctl: power %s does not accept positional arguments\n", action)
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, *clusterName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	modeValue := strings.TrimSpace(*mode)
	if modeValue != "service" && modeValue != "poweroff" {
		_, _ = fmt.Fprintln(stderr, "cgctl: power mode must be service or poweroff")
		return 2
	}
	envelope, err := powerAPIPost(client, serverURL, tokenEnv,
		"/api/v1/clusters/"+url.PathEscape(string(clusterID))+"/power/"+action, map[string]interface{}{"mode": modeValue})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, jsonOutput, envelope, action)
}

func runPowerExecute(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl power execute", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name or UUID")
	approvalToken := flags.String("approval-token", "", "approval token issued by cgctl approval issue")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: power execute does not accept positional arguments")
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, *clusterName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	token := strings.TrimSpace(*approvalToken)
	if token == "" {
		_, _ = fmt.Fprintln(stderr, "cgctl: power execute requires --approval-token")
		return 2
	}
	envelope, err := powerAPIPost(client, serverURL, tokenEnv,
		"/api/v1/clusters/"+url.PathEscape(string(clusterID))+"/power/execute", map[string]interface{}{"approval_token": token})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, jsonOutput, envelope, "execute")
}

func runPowerSimpleAction(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer, action string) int {
	flags := flag.NewFlagSet("cgctl power "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name or UUID")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "cgctl: power %s does not accept positional arguments\n", action)
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, *clusterName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	envelope, err := powerAPIPost(client, serverURL, tokenEnv,
		"/api/v1/clusters/"+url.PathEscape(string(clusterID))+"/power/"+action, map[string]interface{}{})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, jsonOutput, envelope, action)
}

func runPowerFail(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl power fail", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name or UUID")
	reason := flags.String("reason", "", "failure reason recorded on the operation")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: power fail does not accept positional arguments")
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, *clusterName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	envelope, err := powerAPIPost(client, serverURL, tokenEnv,
		"/api/v1/clusters/"+url.PathEscape(string(clusterID))+"/power/fail", map[string]interface{}{"reason": strings.TrimSpace(*reason)})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, jsonOutput, envelope, "fail")
}

func runPowerStatus(arguments []string, stdout io.Writer, stderr io.Writer, serverURL string, tokenEnv string, jsonOutput bool, client httpDoer) int {
	flags := flag.NewFlagSet("cgctl power status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterName := flags.String("cluster", "", "cluster display name or UUID")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "cgctl: power status does not accept positional arguments")
		return 2
	}
	clusterID, err := powerClusterUUID(client, serverURL, tokenEnv, *clusterName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 2
	}
	envelope, err := cgctlAPIGet(client, serverURL, tokenEnv, "/api/v1/clusters/"+url.PathEscape(string(clusterID))+"/power/status")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cgctl:", err)
		return 1
	}
	return powerResponse(stdout, stderr, jsonOutput, envelope, "status")
}

// writePowerHuman renders the power lifecycle result as tab-separated lines
// that follow the other cgctl commands.
func writePowerHuman(writer io.Writer, action string, result json.RawMessage) error {
	var view struct {
		PowerOperation  *model.PowerOperation  `json:"power_operation"`
		BlockingReasons []string               `json:"blocking_reasons"`
		Risk            string                 `json:"risk"`
		Healthy         *bool                  `json:"healthy"`
		Protected       *bool                  `json:"protected"`
		RecoveryFreeze  *bool                  `json:"recovery_freeze"`
		ManualRecovery  *bool                  `json:"manual_recovery_required"`
		Snapshot        *model.PowerSnapshot   `json:"snapshot"`
		Cluster         *model.DatabaseCluster `json:"cluster"`
		Protection      *struct {
			RecoveryFreeze         bool               `json:"recovery_freeze"`
			InstancesInMaintenance []model.ResourceID `json:"instances_in_maintenance"`
		} `json:"protection"`
	}
	if err := json.Unmarshal(result, &view); err != nil {
		return err
	}
	if view.PowerOperation != nil {
		_, _ = fmt.Fprintf(writer, "state\t%s\n", valueOrUnknown(string(view.PowerOperation.State)))
		_, _ = fmt.Fprintf(writer, "mode\t%s\n", valueOrDash(view.PowerOperation.Mode))
		_, _ = fmt.Fprintf(writer, "operation\t%s\trequested_by=%s\n",
			view.PowerOperation.ResourceID, valueOrUnknown(view.PowerOperation.RequestedBy))
	}
	switch action {
	case "precheck":
		reasons := "<none>"
		if len(view.BlockingReasons) > 0 {
			reasons = strings.Join(view.BlockingReasons, "; ")
		}
		_, _ = fmt.Fprintf(writer, "risk\t%s\nblocking_reasons\t%s\n", valueOrDash(view.Risk), reasons)
	case "plan":
		if view.Snapshot != nil {
			_, _ = fmt.Fprintf(writer, "snapshot\tprimary=%s\treplicas=%d\tcaptured_at=%s\n",
				valueOrDash(string(view.Snapshot.Primary.InstanceID)), len(view.Snapshot.Replicas),
				view.Snapshot.CapturedAt.UTC().Format(time.RFC3339))
		}
	case "execute":
		if view.Protection != nil {
			instances := "<none>"
			if len(view.Protection.InstancesInMaintenance) > 0 {
				ids := make([]string, 0, len(view.Protection.InstancesInMaintenance))
				for _, id := range view.Protection.InstancesInMaintenance {
					ids = append(ids, string(id))
				}
				instances = strings.Join(ids, ",")
			}
			_, _ = fmt.Fprintf(writer, "recovery_freeze\t%s\ninstances_in_maintenance\t%s\n",
				yesNo(view.Protection.RecoveryFreeze), instances)
		}
	case "verify":
		_, _ = fmt.Fprintf(writer, "healthy\t%s\n", boolPointerText(view.Healthy))
		reasons := "<none>"
		if len(view.BlockingReasons) > 0 {
			reasons = strings.Join(view.BlockingReasons, "; ")
		}
		_, _ = fmt.Fprintf(writer, "blocking_reasons\t%s\n", reasons)
	case "fail":
		_, _ = fmt.Fprintf(writer, "manual_recovery_required\t%s\n", boolPointerText(view.ManualRecovery))
	case "status":
		if view.Cluster != nil {
			_, _ = fmt.Fprintf(writer, "cluster\t%s\tdisplay=%s\tengine=%s\n",
				view.Cluster.ResourceID, valueOrDash(view.Cluster.DisplayName), view.Cluster.Engine)
		}
		_, _ = fmt.Fprintf(writer, "protected\t%s\nrecovery_freeze\t%s\n", boolPointerText(view.Protected), boolPointerText(view.RecoveryFreeze))
		if view.PowerOperation != nil {
			if !view.PowerOperation.StartedAt.IsZero() {
				_, _ = fmt.Fprintf(writer, "started_at\t%s\n", view.PowerOperation.StartedAt.UTC().Format(time.RFC3339))
			}
			if !view.PowerOperation.CompletedAt.IsZero() {
				_, _ = fmt.Fprintf(writer, "completed_at\t%s\n", view.PowerOperation.CompletedAt.UTC().Format(time.RFC3339))
			}
		}
	}
	return nil
}

func boolPointerText(value *bool) string {
	if value == nil {
		return valueOrUnknown("")
	}
	return yesNo(*value)
}
