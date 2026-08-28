package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"clusterguard.io/ha/internal/platformupdate"
	"clusterguard.io/ha/pkg/model"
)

const softwareUpdateMultipartOverhead = int64(1 << 20)

type SoftwareUpdateManager interface {
	Snapshot(context.Context) platformupdate.Snapshot
	Upload(context.Context, string, io.Reader) (platformupdate.Package, error)
	Start(context.Context, platformupdate.Mode, string, string) (platformupdate.Job, error)
	Package(string) (platformupdate.Package, bool)
	Job(string) (platformupdate.Job, bool)
}

type softwareUpdateActionPayload struct {
	Confirmation string `json:"confirmation,omitempty"`
}

func softwareUpdateRecoveryRoute(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	path = strings.TrimSuffix(path, "/")
	return strings.HasPrefix(path, "/api/v1/platform/updates/") &&
		(strings.HasSuffix(path, "/resume") || strings.HasSuffix(path, "/rollback"))
}

func (server *Server) softwareUpdateRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	if server.softwareUpdates == nil {
		writeError(writer, http.StatusServiceUnavailable, "software update service is not configured")
		return
	}
	tail = strings.Trim(tail, "/")
	if request.Method == http.MethodGet && server.forwardSoftwareUpdateRead(writer, request) {
		return
	}
	if tail == "" {
		switch request.Method {
		case http.MethodGet:
			writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": server.softwareUpdates.Snapshot(request.Context())})
		case http.MethodPost:
			server.uploadSoftwareUpdate(writer, request)
		default:
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	parts := strings.Split(tail, "/")
	if len(parts) == 1 && request.Method == http.MethodGet {
		server.softwareUpdateStatus(writer, parts[0])
		return
	}
	if len(parts) == 2 && request.Method == http.MethodPost {
		mode := platformupdate.Mode(parts[1])
		if mode == platformupdate.ModePlan || mode == platformupdate.ModeExecute ||
			mode == platformupdate.ModeResume || mode == platformupdate.ModeRollback {
			server.startSoftwareUpdate(writer, request, parts[0], mode)
			return
		}
	}
	writeError(writer, http.StatusNotFound, "software update route not found")
}

func (server *Server) uploadSoftwareUpdate(writer http.ResponseWriter, request *http.Request) {
	limit := platformupdate.DefaultMaximumUpload + softwareUpdateMultipartOverhead
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	if err := request.ParseMultipartForm(8 << 20); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeError(writer, http.StatusRequestEntityTooLarge, platformupdate.ErrUploadTooLarge.Error())
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid software update upload")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	files := request.MultipartForm.File["package"]
	if len(files) != 1 {
		writeError(writer, http.StatusBadRequest, "exactly one signed .cgupgrade package is required (.cgpatch is accepted for compatibility)")
		return
	}
	file, err := openMultipartFile(files[0])
	if err != nil {
		writeError(writer, http.StatusBadRequest, "unable to read software update package")
		return
	}
	defer file.Close()
	actor := softwareUpdateActor(request)
	if !server.recordSoftwareUpdateAudit(actor, model.StagePrecheck, "上传并校验软件更新包："+files[0].Filename) {
		writeError(writer, http.StatusServiceUnavailable, "software update audit is unavailable")
		return
	}
	result, err := server.softwareUpdates.Upload(request.Context(), files[0].Filename, file)
	if err != nil {
		server.writeSoftwareUpdateError(writer, err)
		return
	}
	if !server.recordSoftwareUpdateAudit(actor, model.StageAudit, fmt.Sprintf("软件更新包签名校验通过：%s，目标版本：%s", result.PatchID, result.TargetVersion)) {
		writeError(writer, http.StatusServiceUnavailable, "verified update package was stored but audit persistence failed")
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]interface{}{"status": "ok", "result": result, "warning": platformupdate.AutomaticFailoverWarning})
}

func openMultipartFile(header *multipart.FileHeader) (multipart.File, error) {
	if header == nil || !platformupdate.SupportedPackageFileName(header.Filename) {
		return nil, platformupdate.ErrInvalidPatch
	}
	return header.Open()
}

func (server *Server) softwareUpdateStatus(writer http.ResponseWriter, patchID string) {
	packageRecord, found := server.softwareUpdates.Package(patchID)
	if !found {
		writeError(writer, http.StatusNotFound, platformupdate.ErrPackageNotFound.Error())
		return
	}
	result := platformupdate.PackageStatus{Package: packageRecord}
	if job, jobFound := server.softwareUpdates.Job(patchID); jobFound {
		result.Job = &job
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": result, "warning": platformupdate.AutomaticFailoverWarning})
}

func (server *Server) startSoftwareUpdate(writer http.ResponseWriter, request *http.Request, patchID string, mode platformupdate.Mode) {
	payload := softwareUpdateActionPayload{}
	if err := decode(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid software update action")
		return
	}
	actor := softwareUpdateActor(request)
	stage := model.StagePlan
	if mode != platformupdate.ModePlan {
		stage = model.StageExecute
	}
	if !server.recordSoftwareUpdateAudit(actor, stage, fmt.Sprintf("请求软件更新动作：%s，补丁：%s", mode, patchID)) {
		writeError(writer, http.StatusServiceUnavailable, "software update audit is unavailable")
		return
	}
	job, err := server.softwareUpdates.Start(request.Context(), mode, patchID, payload.Confirmation)
	if err != nil {
		server.writeSoftwareUpdateError(writer, err)
		return
	}
	if !server.recordSoftwareUpdateAudit(actor, model.StageAudit, fmt.Sprintf("软件更新动作已进入队列：%s，补丁：%s", mode, patchID)) {
		writeError(writer, http.StatusServiceUnavailable, "software update was queued but audit persistence failed")
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]interface{}{"status": "accepted", "result": job, "warning": platformupdate.AutomaticFailoverWarning})
}

func (server *Server) writeSoftwareUpdateError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, platformupdate.ErrUploadTooLarge):
		writeError(writer, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, platformupdate.ErrPackageNotFound):
		writeError(writer, http.StatusNotFound, err.Error())
	case errors.Is(err, platformupdate.ErrInvalidPatch), errors.Is(err, platformupdate.ErrConfirmationRequired):
		writeError(writer, http.StatusBadRequest, err.Error())
	case errors.Is(err, platformupdate.ErrPlanRequired), errors.Is(err, platformupdate.ErrJobActive), errors.Is(err, platformupdate.ErrPackageConflict):
		writeError(writer, http.StatusConflict, err.Error())
	default:
		writeError(writer, http.StatusServiceUnavailable, err.Error())
	}
}

func softwareUpdateActor(request *http.Request) string {
	if authentication, found := requestAuthentication(request); found && strings.TrimSpace(authentication.principal.Username) != "" {
		return authentication.principal.Username
	}
	return "service-api"
}

func (server *Server) recordSoftwareUpdateAudit(actor string, stage model.WorkflowStage, message string) bool {
	if server.store == nil {
		return false
	}
	now := time.Now().UTC()
	return server.store.RecordAudit(model.AuditEvent{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID(), MetadataRevision: 1, CreatedAt: now, UpdatedAt: now},
		OperationID:  model.NewResourceID(), Stage: stage, Actor: actor, Message: message,
	}) == nil
}

func (server *Server) forwardSoftwareUpdateRead(writer http.ResponseWriter, request *http.Request) bool {
	if server.authority == nil || server.authority.RequireMutationAuthority(request.Context()) == nil {
		return false
	}
	if strings.TrimSpace(request.Header.Get(mutationRPCForwardedHeader)) != "" {
		writeError(writer, http.StatusServiceUnavailable, "software update status requires the current Raft leader")
		return true
	}
	locator, hasLeader := server.authority.(LeaderLocator)
	apiLocator, hasAPI := server.authority.(LeaderAPILocator)
	if !hasLeader || !hasAPI || server.mutationRPC == nil {
		writeError(writer, http.StatusServiceUnavailable, "software update status requires the current Raft leader")
		return true
	}
	leaderID, _, found := locator.Leader()
	if !found {
		writeError(writer, http.StatusServiceUnavailable, "current Raft leader is unknown")
		return true
	}
	leaderAPI, found := apiLocator.LeaderAPIAddress(leaderID)
	if !found || strings.TrimSpace(leaderAPI) == "" {
		writeError(writer, http.StatusServiceUnavailable, "current Raft leader API is unavailable")
		return true
	}
	if err := server.mutationRPC.Forward(writer, request, strings.TrimRight(leaderAPI, "/")); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "current Raft leader is temporarily unreachable")
	}
	return true
}
