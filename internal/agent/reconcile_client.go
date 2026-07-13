package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maximumReconcileResponseBytes = 64 << 10

type HTTPReconcileClient struct {
	controllers []string
	secret      string
	client      *http.Client
	now         func() time.Time
}

func NewHTTPReconcileClient(controllerURLs []string, secret string, client *http.Client, allowInsecureHTTP bool, now func() time.Time) (*HTTPReconcileClient, error) {
	if strings.TrimSpace(secret) == "" || len(controllerURLs) == 0 {
		return nil, fmt.Errorf("agent reconcile controllers and secret are required")
	}
	controllers := make([]string, 0, len(controllerURLs))
	for _, raw := range controllerURLs {
		normalized, err := normalizeControllerURL(raw, allowInsecureHTTP)
		if err != nil {
			return nil, err
		}
		parsed, _ := url.Parse(normalized)
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v1/agent/reconcile"
		controllers = append(controllers, parsed.String())
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &HTTPReconcileClient{controllers: controllers, secret: secret, client: client, now: now}, nil
}

func normalizeControllerURL(raw string, allowInsecureHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("agent reconcile controller URL is invalid")
	}
	if parsed.Scheme != "https" && !(allowInsecureHTTP && parsed.Scheme == "http") {
		return "", fmt.Errorf("agent reconcile controller URL must use HTTPS")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func reconcileNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (client *HTTPReconcileClient) Decision(ctx context.Context, policy ClusterPolicy) (ReconcileResponse, error) {
	if client == nil || client.client == nil {
		return ReconcileResponse{}, fmt.Errorf("agent reconcile HTTP client is not configured")
	}
	nonce, err := reconcileNonce()
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("generate reconcile nonce: %w", err)
	}
	now := client.now().UTC()
	payload := ReconcileRequest{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, RequestedAt: now, Nonce: nonce}
	if err := SignReconcileRequest(&payload, client.secret); err != nil {
		return ReconcileResponse{}, err
	}
	contents, err := json.Marshal(payload)
	if err != nil {
		return ReconcileResponse{}, err
	}
	var failures []error
	for _, controller := range client.controllers {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, controller, bytes.NewReader(contents))
		if requestErr != nil {
			failures = append(failures, requestErr)
			continue
		}
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := client.client.Do(request)
		if requestErr != nil {
			failures = append(failures, requestErr)
			continue
		}
		limited := io.LimitReader(response.Body, maximumReconcileResponseBytes+1)
		responseContents, readErr := io.ReadAll(limited)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			failures = append(failures, errors.Join(readErr, closeErr))
			continue
		}
		if len(responseContents) > maximumReconcileResponseBytes {
			failures = append(failures, fmt.Errorf("controller reconcile response is too large"))
			continue
		}
		if response.StatusCode != http.StatusOK {
			failures = append(failures, fmt.Errorf("controller reconcile status %d", response.StatusCode))
			continue
		}
		decision := ReconcileResponse{}
		decoder := json.NewDecoder(bytes.NewReader(responseContents))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decision); err != nil {
			failures = append(failures, fmt.Errorf("decode controller reconcile response: %w", err))
			continue
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			failures = append(failures, fmt.Errorf("controller reconcile response contains trailing data"))
			continue
		}
		if err := VerifyReconcileResponse(decision, payload, client.secret, now); err != nil {
			failures = append(failures, err)
			continue
		}
		return decision, nil
	}
	return ReconcileResponse{}, fmt.Errorf("no controller returned an authenticated leader decision: %w", errors.Join(failures...))
}
