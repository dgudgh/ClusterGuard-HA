package platformupdate

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var ErrRuntimeIncompatible = errors.New("runtime compatibility floor prohibits this update target")

type compatibilityHelper interface {
	CheckRuntimeCompatibility(context.Context, Mode, string, model.RuntimeCompatibilityFloor) error
}

func (m *Manager) SetRuntimeCompatibilityRequirements(provider func() model.RuntimeCompatibilityFloor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimeRequirements = provider
}
func (m *Manager) CheckRuntimeCompatibility(ctx context.Context, mode Mode, id string, floor model.RuntimeCompatibilityFloor) error {
	if mode == ModePlan {
		return nil
	}
	if !floor.Valid() {
		return fmt.Errorf("%w: unknown minimum protocol", ErrRuntimeIncompatible)
	}
	if !floor.Required() {
		return nil
	}
	checker, ok := m.helper.(compatibilityHelper)
	if !ok {
		return fmt.Errorf("%w: Helper cannot validate actual target programs", ErrRuntimeIncompatible)
	}
	if err := checker.CheckRuntimeCompatibility(ctx, mode, id, floor); err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimeIncompatible, err)
	}
	return nil
}

func (client *UnixHelperClient) CheckRuntimeCompatibility(ctx context.Context, mode Mode, id string, floor model.RuntimeCompatibilityFloor) error {
	contents, err := json.Marshal(helperRequest{Mode: mode, PatchID: id, Requirements: &floor})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/compatibility", bytes.NewReader(contents))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var result struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&result)
		return fmt.Errorf("compatibility preflight HTTP %d: %s", res.StatusCode, result.Message)
	}
	return nil
}

func (m *Manager) RuntimeCompatibilityReady(ctx context.Context) error {
	helper, ok := m.helper.(interface{ RuntimeCompatibilityReady(context.Context) error })
	if !ok {
		return ErrRuntimeIncompatible
	}
	return helper.RuntimeCompatibilityReady(ctx)
}
func (client *UnixHelperClient) RuntimeCompatibilityReady(ctx context.Context) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/runtime-capabilities", nil)
	if e != nil {
		return e
	}
	res, e := client.client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	var info struct {
		Protocol int `json:"runtime_compatibility_protocol"`
		Policy   int `json:"instance_failover_policy_protocol"`
	}
	if res.StatusCode != 200 {
		return ErrRuntimeIncompatible
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&info); e != nil || info.Protocol != model.RuntimeCompatibilityProtocol || info.Policy != model.InstanceFailoverPolicyProtocol {
		return ErrRuntimeIncompatible
	}
	return nil
}
