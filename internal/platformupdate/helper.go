package platformupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type UnixHelperClient struct {
	socketPath string
	client     *http.Client
}

func NewUnixHelperClient(socketPath string) *UnixHelperClient {
	socketPath = strings.TrimSpace(socketPath)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &UnixHelperClient{socketPath: socketPath, client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
}

func (client *UnixHelperClient) Ready(ctx context.Context) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/healthz", nil)
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("helper readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (client *UnixHelperClient) Start(ctx context.Context, mode Mode, patchID string) error {
	contents, _ := json.Marshal(helperRequest{Mode: mode, PatchID: patchID})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/jobs", bytes.NewReader(contents))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		result := struct {
			Message string `json:"message"`
		}{}
		_ = json.NewDecoder(response.Body).Decode(&result)
		if result.Message == "" {
			result.Message = fmt.Sprintf("helper returned HTTP %d", response.StatusCode)
		}
		return errors.New(result.Message)
	}
	return nil
}

type helperRequest struct {
	Mode    Mode   `json:"mode"`
	PatchID string `json:"patch_id"`
}

type JobLauncher interface {
	Start(Mode, string, string, func(error)) error
}

type CommandLauncher struct {
	RunnerPath string
}

func (launcher CommandLauncher) Start(mode Mode, patchID, outputPath string, done func(error)) error {
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// The privileged helper owns this file, while the unprivileged console
	// service needs group-read access to render the live output and history.
	if err := output.Chmod(0o640); err != nil {
		_ = output.Close()
		return err
	}
	command := exec.Command(launcher.RunnerPath, "--mode", string(mode), "--patch-id", patchID)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		_ = output.Close()
		return err
	}
	go func() {
		err := command.Wait()
		_ = output.Close()
		done(err)
	}()
	return nil
}

type HelperHandler struct {
	root     string
	launcher JobLauncher
	mu       sync.Mutex
	active   bool
}

func NewHelperHandler(root string, launcher JobLauncher) *HelperHandler {
	return &HelperHandler{root: filepath.Clean(root), launcher: launcher}
}

func (handler *HelperHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/v1/jobs" {
		helperError(writer, http.StatusNotFound, "route not found")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4096))
	decoder.DisallowUnknownFields()
	payload := helperRequest{}
	if err := decoder.Decode(&payload); err != nil || !validPatchID(payload.PatchID) || !allowedMode(payload.Mode) {
		helperError(writer, http.StatusBadRequest, "invalid software update request")
		return
	}
	directory := filepath.Join(handler.root, payload.PatchID)
	if !strictChild(handler.root, directory) || !regularFile(filepath.Join(directory, patchFileName)) {
		helperError(writer, http.StatusNotFound, "verified software update package was not found")
		return
	}
	handler.mu.Lock()
	if handler.active {
		handler.mu.Unlock()
		helperError(writer, http.StatusConflict, ErrJobActive.Error())
		return
	}
	handler.active = true
	handler.mu.Unlock()
	done := func(err error) {
		if err != nil {
			handler.recordLaunchFailure(payload.PatchID, payload.Mode, err)
		}
		handler.mu.Lock()
		handler.active = false
		handler.mu.Unlock()
	}
	if err := handler.launcher.Start(payload.Mode, payload.PatchID, filepath.Join(directory, outputFileName), done); err != nil {
		done(err)
		helperError(writer, http.StatusInternalServerError, "unable to start privileged software update job")
		return
	}
	writer.WriteHeader(http.StatusAccepted)
	_, _ = writer.Write([]byte(`{"status":"accepted"}`))
}

func (handler *HelperHandler) recordLaunchFailure(patchID string, mode Mode, cause error) {
	path := filepath.Join(handler.root, patchID, jobFileName)
	job := Job{}
	_ = readJSONFile(path, &job)
	now := time.Now().UTC()
	job.PatchID, job.Mode, job.Status = patchID, mode, StatusFailed
	job.Message = "特权更新任务启动或执行失败：" + cause.Error()
	job.Warning = AutomaticFailoverWarning
	job.UpdatedAt, job.FinishedAt = now, now
	_ = writeJSONAtomic(path, job)
}

func helperError(writer http.ResponseWriter, status int, message string) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": "error", "message": message})
}

func allowedMode(mode Mode) bool {
	return mode == ModePlan || mode == ModeExecute || mode == ModeResume || mode == ModeRollback
}

func strictChild(root, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
