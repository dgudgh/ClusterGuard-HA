package configuration

import (
	"bytes"
	"clusterguard.io/ha/internal/consensus"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (p HTTPPeer) call(ctx context.Context, m consensus.ControllerMember, method, path string, body any, out any) error {
	u, e := url.Parse(m.APIAddress)
	if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return fmt.Errorf("invalid controller API address")
	}
	if p.Client == nil || p.Token == "" {
		return fmt.Errorf("controller RPC trust/token not configured")
	}
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.APIAddress, "/")+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	client := *p.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return fmt.Errorf("controller unavailable")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if e != nil {
		return e
	}
	if len(raw) > 256<<10 {
		return fmt.Errorf("controller response too large")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("controller rejected configuration preflight (HTTP %d)", resp.StatusCode)
	}
	if out != nil {
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if e = json.Unmarshal(raw, &envelope); e != nil {
			return e
		}
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}
func (p HTTPPeer) Node(ctx context.Context, m consensus.ControllerMember) (Node, error) {
	var n Node
	e := p.call(ctx, m, http.MethodGet, "/api/v1/control-plane/configuration/node", nil, &n)
	return n, e
}
func (p HTTPPeer) Candidate(ctx context.Context, m consensus.ControllerMember, changes map[string]int) error {
	return p.call(ctx, m, http.MethodPost, "/api/v1/control-plane/configuration/candidate", struct {
		Changes map[string]int `json:"changes"`
	}{changes}, nil)
}
