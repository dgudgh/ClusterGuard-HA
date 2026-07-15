package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ReconcileDecisionCache interface {
	Store(ReconcileResponse) error
	Load(ReconcileRequest, string, time.Time) (ReconcileResponse, error)
}

type FileReconcileDecisionCache struct {
	directory string
}

func NewFileReconcileDecisionCache(directory string) (*FileReconcileDecisionCache, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("reconcile decision cache directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create reconcile decision cache directory: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, fmt.Errorf("secure reconcile decision cache directory: %w", err)
	}
	return &FileReconcileDecisionCache{directory: directory}, nil
}

func (cache *FileReconcileDecisionCache) path(clusterID, instanceID string) (string, error) {
	if cache == nil || cache.directory == "" || !validUUIDPathComponent(clusterID) || !validUUIDPathComponent(instanceID) {
		return "", fmt.Errorf("reconcile decision cache scope is invalid")
	}
	return filepath.Join(cache.directory, clusterID+"-"+instanceID+".json"), nil
}

func validUUIDPathComponent(value string) bool {
	return len(value) == 36 && !strings.ContainsAny(value, `/\\`)
}

func (cache *FileReconcileDecisionCache) Store(response ReconcileResponse) error {
	path, err := cache.path(string(response.ClusterID), string(response.InstanceID))
	if err != nil {
		return err
	}
	contents, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode reconcile decision cache: %w", err)
	}
	temporary, err := os.CreateTemp(cache.directory, ".decision-*.tmp")
	if err != nil {
		return fmt.Errorf("create reconcile decision cache: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("secure reconcile decision cache: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write reconcile decision cache: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync reconcile decision cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close reconcile decision cache: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish reconcile decision cache: %w", err)
	}
	committed = true
	directory, err := os.Open(cache.directory)
	if err != nil {
		return fmt.Errorf("open reconcile decision cache directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync reconcile decision cache directory: %w", err)
	}
	return directory.Close()
}

func (cache *FileReconcileDecisionCache) Load(request ReconcileRequest, secret string, now time.Time) (ReconcileResponse, error) {
	path, err := cache.path(string(request.ClusterID), string(request.InstanceID))
	if err != nil {
		return ReconcileResponse{}, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return ReconcileResponse{}, fmt.Errorf("read reconcile decision cache: %w", err)
	}
	if len(contents) == 0 || len(contents) > maximumReconcileResponseBytes {
		return ReconcileResponse{}, fmt.Errorf("reconcile decision cache size is invalid")
	}
	decision := ReconcileResponse{}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return ReconcileResponse{}, fmt.Errorf("decode reconcile decision cache: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ReconcileResponse{}, fmt.Errorf("reconcile decision cache contains trailing data")
	}
	if err := VerifyReconcileResponse(decision, request, secret, now); err != nil {
		return ReconcileResponse{}, fmt.Errorf("verify reconcile decision cache: %w", err)
	}
	return decision, nil
}
