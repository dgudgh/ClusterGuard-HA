package endpoint

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type GatewayProfile struct {
	URLs     []string `json:"management_urls"`
	CAFile   string   `json:"ca_file"`
	CertFile string   `json:"cert_file"`
	KeyFile  string   `json:"key_file"`
}
type GatewayStatus struct {
	InstanceID string             `json:"instance_id"`
	ClusterID  model.ResourceID   `json:"cluster_id"`
	LastGrant  model.GatewayGrant `json:"last_grant"`
	Active     bool               `json:"active"`
	Targets    map[string]string  `json:"targets"`
}
type GatewayTransport interface {
	Statuses(context.Context, model.ResourceID) ([]GatewayStatus, error)
	Apply(context.Context, model.GatewayGrant, func(context.Context) error) error
}
type gatewayHTTP struct {
	urls   []string
	client *http.Client
}

func NewGatewayHTTP(profile GatewayProfile) (GatewayTransport, error) {
	if len(profile.URLs) < 2 || len(profile.URLs) > 8 {
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
	return &gatewayHTTP{urls: urls, client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}}}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("gateway redirect refused") }}}, nil
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
