package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	mutationReceiptVersion = 1
	mutationStateStarted   = "started"
	mutationStateCompleted = "completed"
	maximumMutationReceipt = 64 * 1024
)

// MutationLedger closes the agent crash window between a host mutation and
// the control plane recording that step. A completed command is replayed;
// an unfinished command is never guessed or executed twice.
type MutationLedger interface {
	Begin(Request, ClusterPolicy) (Response, bool, error)
	Complete(Request, ClusterPolicy, Response) error
}

type FileMutationLedger struct {
	directory string
}

type mutationReceipt struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	Response    Response  `json:"response,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type mutationIntent struct {
	Command          string           `json:"command"`
	Engine           model.Engine     `json:"engine"`
	ClusterID        model.ResourceID `json:"cluster_id"`
	InstanceID       model.ResourceID `json:"instance_id"`
	OperationID      model.ResourceID `json:"operation_id"`
	LeaseID          model.ResourceID `json:"lease_id"`
	PlanDigest       string           `json:"plan_digest"`
	VIP              string           `json:"vip,omitempty"`
	Interface        string           `json:"interface,omitempty"`
	Prefix           int              `json:"prefix,omitempty"`
	ReadOnly         bool             `json:"read_only,omitempty"`
	SourceInstanceID model.ResourceID `json:"source_instance_id,omitempty"`
	SourceNodeID     model.ResourceID `json:"source_node_id,omitempty"`
	SourceHostname   string           `json:"source_hostname,omitempty"`
	SourceIPAddress  string           `json:"source_ip_address,omitempty"`
	SourcePort       int              `json:"source_port,omitempty"`
	OracleTarget     string           `json:"oracle_target,omitempty"`
}

func NewFileMutationLedger(directory string) (*FileMutationLedger, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("mutation state directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create mutation state directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("secure mutation state directory: %w", err)
	}
	return &FileMutationLedger{directory: directory}, nil
}

func mutationFingerprint(request Request, policy ClusterPolicy) (string, error) {
	contents, err := json.Marshal(mutationIntent{
		Command: request.Command, Engine: request.Engine, ClusterID: request.ClusterID, InstanceID: policy.InstanceID,
		OperationID: request.OperationID, LeaseID: request.LeaseID, PlanDigest: request.PlanDigest,
		VIP: request.VIP, Interface: request.Interface, Prefix: request.Prefix, ReadOnly: request.ReadOnly,
		SourceInstanceID: request.SourceInstanceID, SourceNodeID: request.SourceNodeID, SourceHostname: request.SourceHostname,
		SourceIPAddress: request.SourceIPAddress, SourcePort: request.SourcePort, OracleTarget: request.OracleTarget,
	})
	if err != nil {
		return "", fmt.Errorf("encode mutation intent: %w", err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func (ledger *FileMutationLedger) receiptPath(request Request, policy ClusterPolicy) (string, error) {
	if ledger == nil || ledger.directory == "" || !model.ValidResourceID(request.ClusterID) ||
		!model.ValidResourceID(request.OperationID) || !model.ValidResourceID(policy.InstanceID) || strings.TrimSpace(request.Command) == "" {
		return "", fmt.Errorf("mutation receipt scope is invalid")
	}
	key := sha256.Sum256([]byte(strings.Join([]string{
		string(request.ClusterID), string(policy.InstanceID), string(request.OperationID), request.Command,
	}, "\x00")))
	return filepath.Join(ledger.directory, hex.EncodeToString(key[:])+".json"), nil
}

func (ledger *FileMutationLedger) Begin(request Request, policy ClusterPolicy) (Response, bool, error) {
	path, err := ledger.receiptPath(request, policy)
	if err != nil {
		return Response{}, false, err
	}
	fingerprint, err := mutationFingerprint(request, policy)
	if err != nil {
		return Response{}, false, err
	}
	receipt := mutationReceipt{
		Version: mutationReceiptVersion, Fingerprint: fingerprint,
		State: mutationStateStarted, UpdatedAt: time.Now().UTC(),
	}
	contents, err := json.Marshal(receipt)
	if err != nil {
		return Response{}, false, fmt.Errorf("encode mutation receipt: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if writeErr := writeNewMutationReceipt(file, contents); writeErr != nil {
			_ = os.Remove(path)
			return Response{}, false, writeErr
		}
		if err := syncMutationDirectory(ledger.directory); err != nil {
			return Response{}, false, err
		}
		return Response{}, false, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return Response{}, false, fmt.Errorf("create mutation receipt: %w", err)
	}
	existing, err := ledger.load(path)
	if err != nil {
		return Response{}, false, err
	}
	if existing.Fingerprint != fingerprint {
		return Response{}, false, fmt.Errorf("existing mutation intent does not match the signed request")
	}
	switch existing.State {
	case mutationStateCompleted:
		return existing.Response, true, nil
	case mutationStateStarted:
		return Response{}, false, fmt.Errorf("previous mutation outcome is unknown; operator verification is required")
	default:
		return Response{}, false, fmt.Errorf("mutation receipt state is invalid")
	}
}

func writeNewMutationReceipt(file *os.File, contents []byte) error {
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure mutation receipt: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write mutation receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync mutation receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close mutation receipt: %w", err)
	}
	return nil
}

func (ledger *FileMutationLedger) Complete(request Request, policy ClusterPolicy, response Response) error {
	path, err := ledger.receiptPath(request, policy)
	if err != nil {
		return err
	}
	fingerprint, err := mutationFingerprint(request, policy)
	if err != nil {
		return err
	}
	existing, err := ledger.load(path)
	if err != nil {
		return err
	}
	if existing.Fingerprint != fingerprint {
		return fmt.Errorf("existing mutation intent does not match the signed request")
	}
	if existing.State == mutationStateCompleted {
		return nil
	}
	if existing.State != mutationStateStarted {
		return fmt.Errorf("mutation receipt state is invalid")
	}
	receipt := mutationReceipt{
		Version: mutationReceiptVersion, Fingerprint: fingerprint, State: mutationStateCompleted,
		Response: response, UpdatedAt: time.Now().UTC(),
	}
	contents, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode completed mutation receipt: %w", err)
	}
	temporary, err := os.CreateTemp(ledger.directory, ".mutation-*.tmp")
	if err != nil {
		return fmt.Errorf("create completed mutation receipt: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeNewMutationReceipt(temporary, contents); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish completed mutation receipt: %w", err)
	}
	committed = true
	return syncMutationDirectory(ledger.directory)
}

func (ledger *FileMutationLedger) load(path string) (mutationReceipt, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return mutationReceipt{}, fmt.Errorf("inspect mutation receipt: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumMutationReceipt {
		return mutationReceipt{}, fmt.Errorf("mutation receipt is not a valid regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return mutationReceipt{}, fmt.Errorf("read mutation receipt: %w", err)
	}
	receipt := mutationReceipt{}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return mutationReceipt{}, fmt.Errorf("decode mutation receipt: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return mutationReceipt{}, fmt.Errorf("mutation receipt contains trailing data")
	}
	if receipt.Version != mutationReceiptVersion || receipt.Fingerprint == "" {
		return mutationReceipt{}, fmt.Errorf("mutation receipt version or fingerprint is invalid")
	}
	return receipt, nil
}

func syncMutationDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open mutation state directory: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync mutation state directory: %w", err)
	}
	return nil
}
