package main

import (
	"bytes"
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
)

const maximumAPIResponseBytes = 8 << 20

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
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
		return "", "", fmt.Errorf("command is required: engines, clusters, topology, health, candidates, metrics, refresh, operation, approval")
	}
	command := arguments[0]
	switch command {
	case "engines", "clusters":
		if len(arguments) != 1 {
			return "", "", fmt.Errorf("%s does not accept arguments", command)
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
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	method, path, err := requestFor(flags.Args())
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
		_, _ = fmt.Fprintln(stderr, "cgctl:", message)
		return 1
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
