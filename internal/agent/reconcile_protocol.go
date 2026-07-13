package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type ReconcileAction string

const (
	ReconcileKeepVIP     ReconcileAction = "keep_vip"
	ReconcileSelfIsolate ReconcileAction = "release_and_read_only"
)

type ReconcileRequest struct {
	ClusterID   model.ResourceID `json:"cluster_id"`
	InstanceID  model.ResourceID `json:"instance_id"`
	RequestedAt time.Time        `json:"requested_at"`
	Nonce       string           `json:"nonce"`
	Signature   string           `json:"signature"`
}

type ReconcileResponse struct {
	ClusterID    model.ResourceID `json:"cluster_id"`
	InstanceID   model.ResourceID `json:"instance_id"`
	Action       ReconcileAction  `json:"action"`
	Reason       string           `json:"reason"`
	LeaseID      model.ResourceID `json:"lease_id,omitempty"`
	ValidUntil   time.Time        `json:"valid_until"`
	ControllerID model.ResourceID `json:"controller_id"`
	Signature    string           `json:"signature"`
}

type unsignedReconcileRequest struct {
	ClusterID   model.ResourceID `json:"cluster_id"`
	InstanceID  model.ResourceID `json:"instance_id"`
	RequestedAt time.Time        `json:"requested_at"`
	Nonce       string           `json:"nonce"`
}

type unsignedReconcileResponse struct {
	ClusterID    model.ResourceID `json:"cluster_id"`
	InstanceID   model.ResourceID `json:"instance_id"`
	Action       ReconcileAction  `json:"action"`
	Reason       string           `json:"reason"`
	LeaseID      model.ResourceID `json:"lease_id,omitempty"`
	ValidUntil   time.Time        `json:"valid_until"`
	ControllerID model.ResourceID `json:"controller_id"`
}

func reconcileMAC(value interface{}, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("agent reconcile secret is required")
	}
	contents, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(contents)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func SignReconcileRequest(request *ReconcileRequest, secret string) error {
	if request == nil {
		return fmt.Errorf("agent reconcile request is required")
	}
	request.RequestedAt = request.RequestedAt.UTC()
	signature, err := reconcileMAC(unsignedReconcileRequest{ClusterID: request.ClusterID, InstanceID: request.InstanceID, RequestedAt: request.RequestedAt, Nonce: request.Nonce}, secret)
	if err != nil {
		return err
	}
	request.Signature = signature
	return nil
}

func VerifyReconcileRequest(request ReconcileRequest, secret string, now time.Time) error {
	if !model.ValidResourceID(request.ClusterID) || !model.ValidResourceID(request.InstanceID) || len(request.Nonce) < 16 || len(request.Nonce) > 128 {
		return fmt.Errorf("agent reconcile request scope is invalid")
	}
	age := now.UTC().Sub(request.RequestedAt.UTC())
	if age < -30*time.Second || age > 30*time.Second {
		return fmt.Errorf("agent reconcile request is outside the allowed time window")
	}
	expected, err := reconcileMAC(unsignedReconcileRequest{ClusterID: request.ClusterID, InstanceID: request.InstanceID, RequestedAt: request.RequestedAt.UTC(), Nonce: request.Nonce}, secret)
	if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(request.Signature))) != 1 {
		return fmt.Errorf("agent reconcile request signature is invalid")
	}
	return nil
}

func SignReconcileResponse(response *ReconcileResponse, secret string) error {
	if response == nil {
		return fmt.Errorf("agent reconcile response is required")
	}
	response.Reason = strings.TrimSpace(response.Reason)
	response.ValidUntil = response.ValidUntil.UTC()
	signature, err := reconcileMAC(unsignedReconcileResponse{
		ClusterID: response.ClusterID, InstanceID: response.InstanceID, Action: response.Action, Reason: response.Reason,
		LeaseID: response.LeaseID, ValidUntil: response.ValidUntil, ControllerID: response.ControllerID,
	}, secret)
	if err != nil {
		return err
	}
	response.Signature = signature
	return nil
}

func VerifyReconcileResponse(response ReconcileResponse, request ReconcileRequest, secret string, now time.Time) error {
	if response.ClusterID != request.ClusterID || response.InstanceID != request.InstanceID || !model.ValidResourceID(response.ControllerID) || strings.TrimSpace(response.Reason) == "" {
		return fmt.Errorf("agent reconcile response scope is invalid")
	}
	if response.Action != ReconcileKeepVIP && response.Action != ReconcileSelfIsolate {
		return fmt.Errorf("agent reconcile response action is invalid")
	}
	if response.Action == ReconcileKeepVIP && !model.ValidResourceID(response.LeaseID) {
		return fmt.Errorf("keep-VIP response requires a lease UUID")
	}
	if !response.ValidUntil.After(now.UTC()) || response.ValidUntil.After(now.UTC().Add(time.Minute)) {
		return fmt.Errorf("agent reconcile response is expired or outside the allowed time window")
	}
	expected, err := reconcileMAC(unsignedReconcileResponse{
		ClusterID: response.ClusterID, InstanceID: response.InstanceID, Action: response.Action, Reason: strings.TrimSpace(response.Reason),
		LeaseID: response.LeaseID, ValidUntil: response.ValidUntil.UTC(), ControllerID: response.ControllerID,
	}, secret)
	if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(response.Signature))) != 1 {
		return fmt.Errorf("agent reconcile response signature is invalid")
	}
	return nil
}
