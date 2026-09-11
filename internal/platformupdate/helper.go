package platformupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	directory, err := openHelperDirectory(filepath.Dir(outputPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	output, err := openHelperFile(directory, filepath.Base(outputPath), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
	root        string
	privateRoot string
	launcher    JobLauncher
	mu          sync.Mutex
	active      bool
}

func NewHelperHandler(root string, launcher JobLauncher) *HelperHandler {
	// Kept for unit tests and embedders that intentionally use a single
	// already-isolated directory. The packaged helper uses the explicit private
	// constructor below.
	return &HelperHandler{root: filepath.Clean(root), launcher: launcher}
}

func NewHelperHandlerWithPrivateRoot(root, privateRoot string, launcher JobLauncher) *HelperHandler {
	return &HelperHandler{root: filepath.Clean(root), privateRoot: filepath.Clean(privateRoot), launcher: launcher}
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
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		helperError(writer, http.StatusBadRequest, "invalid software update request")
		return
	}
	directory := filepath.Join(handler.root, payload.PatchID)
	if !strictChild(handler.root, directory) {
		helperError(writer, http.StatusNotFound, "verified software update package was not found")
		return
	}
	jobDirectory, err := openHelperDirectory(directory)
	if err != nil {
		helperError(writer, http.StatusNotFound, "verified software update package was not found")
		return
	}
	patch, err := openHelperFile(jobDirectory, patchFileName, os.O_RDONLY, 0)
	if err != nil {
		_ = jobDirectory.Close()
		helperError(writer, http.StatusNotFound, "verified software update package was not found")
		return
	}
	defer patch.Close()
	if err := jobDirectory.Chmod(updateJobDirMode); err != nil {
		_ = jobDirectory.Close()
		helperError(writer, http.StatusInternalServerError, "unable to prepare software update directory")
		return
	}
	if err := patch.Chmod(updateFileMode); err != nil {
		_ = jobDirectory.Close()
		helperError(writer, http.StatusInternalServerError, "unable to prepare software update package")
		return
	}
	privatePath := directory
	privateDirectory := jobDirectory
	privateSeparate := false
	if handler.privateRoot != "" {
		privatePath = filepath.Join(handler.privateRoot, "jobs", payload.PatchID)
		privateDirectory, err = openTrustedWorkspaceDirectory(privatePath, true)
		if err != nil {
			_ = jobDirectory.Close()
			helperError(writer, http.StatusInternalServerError, "unable to prepare private software update workspace")
			return
		}
		privateSeparate = true
	}
	handler.mu.Lock()
	if handler.active {
		handler.mu.Unlock()
		_ = jobDirectory.Close()
		if privateSeparate {
			_ = privateDirectory.Close()
		}
		helperError(writer, http.StatusConflict, ErrJobActive.Error())
		return
	}
	handler.active = true
	handler.mu.Unlock()
	var finished sync.Once
	done := func(err error) {
		finished.Do(func() {
			defer jobDirectory.Close()
			if privateSeparate {
				defer privateDirectory.Close()
			}
			if err != nil {
				handler.recordLaunchFailure(privateDirectory, payload.PatchID, payload.Mode, err)
			}
			handler.mu.Lock()
			handler.active = false
			handler.mu.Unlock()
		})
	}
	if err := handler.launcher.Start(payload.Mode, payload.PatchID, filepath.Join(privatePath, outputFileName), done); err != nil {
		done(err)
		helperError(writer, http.StatusInternalServerError, "unable to start privileged software update job")
		return
	}
	writer.WriteHeader(http.StatusAccepted)
	_, _ = writer.Write([]byte(`{"status":"accepted"}`))
}

func (handler *HelperHandler) recordLaunchFailure(directory *os.File, patchID string, mode Mode, cause error) {
	job := Job{}
	file, err := openHelperFile(directory, jobFileName, os.O_RDONLY, 0)
	if err == nil {
		contents, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			return
		}
		if err := json.Unmarshal(contents, &job); err != nil {
			job = Job{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	// The job runner writes the authoritative terminal state. A non-zero exit is
	// expected after a safe preflight block or a completed automatic rollback,
	// so the helper must not replace that evidence with a generic launch error.
	switch job.Status {
	case StatusPlanned, StatusSucceeded, StatusFailed, StatusRolledBack:
		return
	}
	now := time.Now().UTC()
	job.PatchID, job.Mode, job.Status = patchID, mode, StatusFailed
	job.Message = "特权更新任务启动或执行失败：" + cause.Error()
	job.Warning = AutomaticFailoverWarning
	job.UpdatedAt, job.FinishedAt = now, now
	_ = writeHelperJob(directory, job)
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
