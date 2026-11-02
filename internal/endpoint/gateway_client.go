package endpoint

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type GatewayProfile struct {
	URLs               []string `json:"management_urls"`
	AllowSingleGateway bool     `json:"allow_single_gateway,omitempty"`
	CAFile             string   `json:"ca_file"`
	CertFile           string   `json:"cert_file"`
	KeyFile            string   `json:"key_file"`
}
type GatewayStatus struct {
	InstanceID    string `json:"instance_id"`
	managementURL string
	ClusterID     model.ResourceID   `json:"cluster_id"`
	LastGrant     model.GatewayGrant `json:"last_grant"`
	Active        bool               `json:"active"`
	Targets       map[string]string  `json:"targets"`
}
type GatewayTransport interface {
	Statuses(context.Context, model.ResourceID) ([]GatewayStatus, error)
	Apply(context.Context, model.GatewayGrant, func(context.Context) error) error
}
type gatewayHTTP struct {
	urls    []string
	client  *http.Client
	minimum int
}

func NewGatewayHTTP(profile GatewayProfile) (GatewayTransport, error) {
	minimum := 2
	if profile.AllowSingleGateway {
		minimum = 1
		if len(profile.URLs) != 1 {
			return nil, fmt.Errorf("single gateway mode requires exactly one management URL")
		}
	}
	if len(profile.URLs) < minimum || len(profile.URLs) > 8 {
		return nil, fmt.Errorf("gateway profile requires 2..8 management URLs")
	}
	seen := map[string]bool{}
	urls := []string{}
	for _, address := range profile.URLs {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("invalid gateway management URL")
		}
		base := strings.TrimRight(u.String(), "/")
		if seen[base] {
			return nil, fmt.Errorf("duplicate gateway")
		}
		seen[base] = true
		urls = append(urls, base)
	}
	cert, err := tls.LoadX509KeyPair(profile.CertFile, profile.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load gateway client certificate: %w", err)
	}
	data, err := os.ReadFile(profile.CAFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("invalid gateway CA")
	}
	return &gatewayHTTP{urls: urls, minimum: minimum, client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}}}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("gateway redirect refused") }}}, nil
}
func (c *gatewayHTTP) request(ctx context.Context, base, path string, grant *model.GatewayGrant) (GatewayStatus, error) {
	var input []byte
	method := http.MethodGet
	if grant != nil {
		var err error
		input, err = json.Marshal(grant)
		if err != nil {
			return GatewayStatus{}, err
		}
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(input))
	if err != nil {
		return GatewayStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return GatewayStatus{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 || resp.StatusCode != http.StatusOK {
		return GatewayStatus{}, fmt.Errorf("gateway response failed: HTTP %d", resp.StatusCode)
	}
	var result GatewayStatus
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}
func (c *gatewayHTTP) Statuses(ctx context.Context, cluster model.ResourceID) ([]GatewayStatus, error) {
	results := []GatewayStatus{}
	for _, base := range c.urls {
		result, err := c.request(ctx, base, "/v1/status?cluster_id="+url.QueryEscape(string(cluster)), nil)
		if err != nil {
			return nil, err
		}
		if result.ClusterID != cluster {
			return nil, fmt.Errorf("gateway cluster mismatch")
		}
		results = append(results, result)
	}
	return results, nil
}
func sameGatewayGrant(a, b model.GatewayGrant) bool {
	a.IssuedAt = a.IssuedAt.UTC()
	b.IssuedAt = b.IssuedAt.UTC()
	a.ExpiresAt = a.ExpiresAt.UTC()
	b.ExpiresAt = b.ExpiresAt.UTC()
	return a == b
}
func (c *gatewayHTTP) Apply(ctx context.Context, grant model.GatewayGrant, validate func(context.Context) error) error {
	var first error
	for _, base := range c.urls {
		if err := validate(ctx); err != nil {
			return err
		}
		result, err := c.request(ctx, base, "/v1/route", &grant)
		if err == nil && (result.ClusterID != grant.ClusterID || !sameGatewayGrant(result.LastGrant, grant) || result.Active != (grant.Mode == "activate")) {
			err = fmt.Errorf("gateway acknowledgement mismatch")
		}
		if err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return first
	}
	return validate(ctx)
}

// GatewayAvailability is incomplete only for network failures. Invalid TLS,
// identities and malformed responses never become an availability exception.
type GatewayAvailability struct {
	Statuses    []GatewayStatus
	Unavailable int
}
type availableGateway struct {
	base   string
	status GatewayStatus
}

func (c *gatewayHTTP) MinimumGatewayObservations() int { return c.minimum }
func gatewayNetworkFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Op == "dial" || op.Op == "read" || op.Op == "write"
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout() || errors.Is(err, io.EOF)
}
func (c *gatewayHTTP) available(ctx context.Context, cluster model.ResourceID) ([]availableGateway, int, error) {
	type observation struct {
		index  int
		status GatewayStatus
		err    error
	}
	replies := make(chan observation, len(c.urls))
	for index, base := range c.urls {
		go func(index int, base string) {
			status, err := c.request(ctx, base, "/v1/status?cluster_id="+url.QueryEscape(string(cluster)), nil)
			replies <- observation{index, status, err}
		}(index, base)
	}
	received := make([]observation, len(c.urls))
	for range c.urls {
		r := <-replies
		received[r.index] = r
	}
	var available []availableGateway
	missing := 0
	for _, r := range received {
		if r.err != nil {
			if gatewayNetworkFailure(ctx, r.err) {
				missing++
				continue
			}
			return nil, missing, r.err
		}
		if r.status.ClusterID != cluster {
			return nil, missing, fmt.Errorf("gateway cluster mismatch")
		}
		r.status.managementURL = c.urls[r.index]
		available = append(available, availableGateway{base: c.urls[r.index], status: r.status})
	}
	if len(available) == 0 {
		return nil, missing, fmt.Errorf("no gateway observation available")
	}
	return available, missing, nil
}
func (c *gatewayHTTP) AvailableStatuses(ctx context.Context, cluster model.ResourceID) (GatewayAvailability, error) {
	states, missing, err := c.available(ctx, cluster)
	result := GatewayAvailability{Unavailable: missing}
	for _, state := range states {
		result.Statuses = append(result.Statuses, state.status)
	}
	return result, err
}

// RenewStable cannot change a route's identity or revive a restarted gateway.
// It never rolls back another live gateway on a partial renewal error.
func (c *gatewayHTTP) RenewStable(ctx context.Context, grant model.GatewayGrant, expected []GatewayStatus, validate func(context.Context) error) (int, error) {
	allowed := map[string]bool{}
	for _, base := range c.urls {
		allowed[base] = true
	}
	var states []availableGateway
	for _, prior := range expected {
		if !allowed[prior.managementURL] {
			return 0, fmt.Errorf("stable renewal has no authenticated observation source")
		}
		current, err := c.request(ctx, prior.managementURL, "/v1/status?cluster_id="+url.QueryEscape(string(grant.ClusterID)), nil)
		if err != nil {
			return 0, err
		}
		states = append(states, availableGateway{base: prior.managementURL, status: current})
	}
	missing := len(c.urls) - len(states)
	if len(states) == 0 {
		return 0, fmt.Errorf("no authenticated stable gateway available")
	}
	var err error
	seen := map[string]bool{}
	for _, state := range states {
		known := false
		for _, previous := range expected {
			if previous.InstanceID == state.status.InstanceID {
				known = true
			}
		}
		old := state.status.LastGrant
		if state.status.ClusterID != grant.ClusterID || old.ClusterID != grant.ClusterID || !known || state.status.InstanceID == "" || seen[state.status.InstanceID] || !state.status.Active || old.Mode != "activate" || !old.ExpiresAt.After(time.Now()) || grant.Mode != "activate" || old.Epoch != grant.Epoch || old.LeaseID != grant.LeaseID || old.TargetID != grant.TargetID || old.OperationID != grant.OperationID || old.Sequence >= grant.Sequence {
			return 0, fmt.Errorf("stable renewal identity changed")
		}
		seen[state.status.InstanceID] = true
	}
	updated := 0
	var failures []error
	if missing > 0 {
		failures = append(failures, fmt.Errorf("%d gateway(s) unavailable during stable renewal", missing))
	}
	for _, state := range states {
		if err = validate(ctx); err != nil {
			return updated, errors.Join(append(failures, err)...)
		}
		result, e := c.request(ctx, state.base, "/v1/route", &grant)
		if e == nil && (result.InstanceID != state.status.InstanceID || result.ClusterID != grant.ClusterID || !result.Active || !sameGatewayGrant(result.LastGrant, grant)) {
			e = fmt.Errorf("stable renewal acknowledgement mismatch")
		}
		if e != nil {
			failures = append(failures, e)
		} else {
			updated++
		}
	}
	if err = validate(ctx); err != nil {
		failures = append(failures, err)
	}
	if updated == 0 {
		failures = append(failures, fmt.Errorf("no gateway confirmed stable renewal"))
	}
	return updated, errors.Join(failures...)
}
