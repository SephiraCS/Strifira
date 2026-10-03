package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/validate"
	"github.com/labstack/echo/v4"
)

const (
	devBoxUserBodyLimit      = 128 << 10
	devBoxHeartbeatBodyLimit = 8 << 10
	// JSON can encode each decoded byte as a six-byte Unicode escape. Leave
	// room for the immutable lease, sequence and envelope as well.
	devBoxLogBodyLimit = 6*service.DevBoxMaxLogEntryBytes + 1024
)

type devBoxService interface {
	Create(context.Context, uuid.UUID, uuid.UUID, dto.CreateDevBoxRequest) (*dto.DevBoxTokenResponse, error)
	List(context.Context, uuid.UUID, uuid.UUID) ([]domain.DevBox, error)
	Update(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.UpdateDevBoxRequest) (*domain.DevBox, error)
	RotateToken(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*dto.DevBoxTokenResponse, error)
	Revoke(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	CreateJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.CreateDevBoxJobRequest) (*domain.DevBoxJob, error)
	ListJobs(context.Context, uuid.UUID, uuid.UUID, dto.DevBoxJobListParams) ([]domain.DevBoxJob, int, error)
	GetJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	CancelJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	JobLogs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.DevBoxJobLogParams) ([]domain.DevBoxJobLog, error)
	Heartbeat(context.Context, *domain.DevBox, dto.DevBoxHeartbeatRequest) (*dto.DevBoxHeartbeatResponse, error)
	Claim(context.Context, *domain.DevBox) (*dto.DevBoxClaimResponse, error)
	Start(context.Context, *domain.DevBox, uuid.UUID, dto.DevBoxLeaseRequest) (*dto.DevBoxLeaseResponse, error)
	Renew(context.Context, *domain.DevBox, uuid.UUID, dto.DevBoxLeaseRequest) (*dto.DevBoxLeaseResponse, error)
	AppendLog(context.Context, *domain.DevBox, uuid.UUID, dto.DevBoxAppendLogRequest) (*dto.DevBoxAppendLogResponse, error)
	Complete(context.Context, *domain.DevBox, uuid.UUID, dto.DevBoxCompleteRequest) (*domain.DevBoxJob, error)
}

type DevBoxHandler struct {
	service devBoxService
}

func NewDevBoxHandler(boxService devBoxService) *DevBoxHandler {
	return &DevBoxHandler{service: boxService}
}

func (h *DevBoxHandler) Create(c echo.Context) error {
	var request dto.CreateDevBoxRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxUserBodyLimit); !ok {
		return err
	}
	result, err := h.service.Create(c.Request().Context(), middleware.GetWorkspace(c).ID, middleware.GetUserID(c), request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusCreated, result)
}

func (h *DevBoxHandler) List(c echo.Context) error {
	boxes, err := h.service.List(c.Request().Context(), middleware.GetWorkspace(c).ID, middleware.GetUserID(c))
	if err != nil {
		return devBoxError(c, err)
	}
	if boxes == nil {
		boxes = []domain.DevBox{}
	}
	return response.Success(c, http.StatusOK, boxes)
}

func (h *DevBoxHandler) Update(c echo.Context) error {
	boxID, ok, err := devBoxParamID(c, "boxId", "dev box")
	if !ok {
		return err
	}
	var request dto.UpdateDevBoxRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxUserBodyLimit); !ok {
		return err
	}
	box, err := h.service.Update(c.Request().Context(), middleware.GetWorkspace(c).ID, boxID, middleware.GetUserID(c), request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, box)
}

func (h *DevBoxHandler) RotateToken(c echo.Context) error {
	boxID, ok, err := devBoxParamID(c, "boxId", "dev box")
	if !ok {
		return err
	}
	result, err := h.service.RotateToken(c.Request().Context(), middleware.GetWorkspace(c).ID, boxID, middleware.GetUserID(c))
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *DevBoxHandler) Revoke(c echo.Context) error {
	boxID, ok, err := devBoxParamID(c, "boxId", "dev box")
	if !ok {
		return err
	}
	if err := h.service.Revoke(c.Request().Context(), middleware.GetWorkspace(c).ID, boxID, middleware.GetUserID(c)); err != nil {
		return devBoxError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *DevBoxHandler) CreateJob(c echo.Context) error {
	boxID, ok, err := devBoxParamID(c, "boxId", "dev box")
	if !ok {
		return err
	}
	var request dto.CreateDevBoxJobRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxUserBodyLimit); !ok {
		return err
	}
	job, err := h.service.CreateJob(c.Request().Context(), middleware.GetWorkspace(c).ID, boxID, middleware.GetUserID(c), request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusAccepted, job)
}

func (h *DevBoxHandler) ListJobs(c echo.Context) error {
	var params dto.DevBoxJobListParams
	if err := c.Bind(&params); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid query parameters")
	}
	params.Defaults()
	if err := validate.Struct(&params); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid job query parameters")
	}
	jobs, total, err := h.service.ListJobs(c.Request().Context(), middleware.GetWorkspace(c).ID, middleware.GetUserID(c), params)
	if err != nil {
		return devBoxError(c, err)
	}
	if jobs == nil {
		jobs = []domain.DevBoxJob{}
	}
	return response.Success(c, http.StatusOK, dto.ListResponse[domain.DevBoxJob]{
		Data: jobs, TotalCount: total, Page: params.Page, PerPage: params.PerPage,
		HasMore: params.Page*params.PerPage < total,
	})
}

func (h *DevBoxHandler) GetJob(c echo.Context) error {
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	job, err := h.service.GetJob(c.Request().Context(), middleware.GetWorkspace(c).ID, jobID, middleware.GetUserID(c))
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, job)
}

func (h *DevBoxHandler) CancelJob(c echo.Context) error {
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	job, err := h.service.CancelJob(c.Request().Context(), middleware.GetWorkspace(c).ID, jobID, middleware.GetUserID(c))
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, job)
}

func (h *DevBoxHandler) JobLogs(c echo.Context) error {
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	var params dto.DevBoxJobLogParams
	if err := c.Bind(&params); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid query parameters")
	}
	if err := validate.Struct(&params); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid log query parameters")
	}
	logs, err := h.service.JobLogs(c.Request().Context(), middleware.GetWorkspace(c).ID, jobID, middleware.GetUserID(c), params)
	if err != nil {
		return devBoxError(c, err)
	}
	if logs == nil {
		logs = []domain.DevBoxJobLog{}
	}
	return response.Success(c, http.StatusOK, logs)
}

func (h *DevBoxHandler) Heartbeat(c echo.Context) error {
	box := middleware.GetDevBox(c)
	if box == nil {
		return response.Unauthorized(c)
	}
	var request dto.DevBoxHeartbeatRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxHeartbeatBodyLimit); !ok {
		return err
	}
	result, err := h.service.Heartbeat(c.Request().Context(), box, request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *DevBoxHandler) Claim(c echo.Context) error {
	box := middleware.GetDevBox(c)
	if box == nil {
		return response.Unauthorized(c)
	}
	result, err := h.service.Claim(c.Request().Context(), box)
	if err != nil {
		return devBoxError(c, err)
	}
	if result == nil {
		return c.NoContent(http.StatusNoContent)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *DevBoxHandler) Start(c echo.Context) error {
	return h.devBoxLease(c, false)
}

func (h *DevBoxHandler) Renew(c echo.Context) error {
	return h.devBoxLease(c, true)
}

func (h *DevBoxHandler) devBoxLease(c echo.Context, renew bool) error {
	box := middleware.GetDevBox(c)
	if box == nil {
		return response.Unauthorized(c)
	}
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	var request dto.DevBoxLeaseRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxUserBodyLimit); !ok {
		return err
	}
	var result *dto.DevBoxLeaseResponse
	if renew {
		result, err = h.service.Renew(c.Request().Context(), box, jobID, request)
	} else {
		result, err = h.service.Start(c.Request().Context(), box, jobID, request)
	}
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *DevBoxHandler) AppendLog(c echo.Context) error {
	box := middleware.GetDevBox(c)
	if box == nil {
		return response.Unauthorized(c)
	}
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	var request dto.DevBoxAppendLogRequest
	if ok, err := bindDevBoxRequest(c, &request, devBoxLogBodyLimit); !ok {
		return err
	}
	result, err := h.service.AppendLog(c.Request().Context(), box, jobID, request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *DevBoxHandler) Complete(c echo.Context) error {
	box := middleware.GetDevBox(c)
	if box == nil {
		return response.Unauthorized(c)
	}
	jobID, ok, err := devBoxParamID(c, "jobId", "job")
	if !ok {
		return err
	}
	var request dto.DevBoxCompleteRequest
	if ok, err := bindDevBoxRequest(c, &request, dto.DevBoxCompleteBodyLimit); !ok {
		return err
	}
	job, err := h.service.Complete(c.Request().Context(), box, jobID, request)
	if err != nil {
		return devBoxError(c, err)
	}
	return response.Success(c, http.StatusOK, job)
}

func devBoxParamID(c echo.Context, name, resource string) (uuid.UUID, bool, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false, response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid "+resource+" ID")
	}
	return id, true, nil
}

// bindDevBoxRequest reads the complete bounded body before using the shared
// binder, including trailing bytes and requests without a Content-Length.
func bindDevBoxRequest(c echo.Context, request any, maxBytes int64) (bool, error) {
	limited := http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return false, response.Error(c, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body exceeds the size limit")
		}
		return false, response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	c.Request().ContentLength = int64(len(body))
	if err := bindAndValidate(c, request); err != nil {
		return false, err
	}
	// The shared helper can write an error response and return nil.
	return !c.Response().Committed, nil
}

func devBoxError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrDevBoxNotFound):
		return response.NotFound(c, "Dev box or job")
	case errors.Is(err, service.ErrDevBoxConflict):
		return response.Error(c, http.StatusConflict, "DEV_BOX_CONFLICT", "Dev box state conflicts with this request")
	case errors.Is(err, service.ErrDevBoxLeaseLost):
		return response.Error(c, http.StatusConflict, "LEASE_LOST", "Job lease is no longer valid")
	case errors.Is(err, service.ErrInvalidDevBoxInput):
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid dev box request")
	case errors.Is(err, service.ErrDevBoxForbidden):
		return response.Forbidden(c)
	case errors.Is(err, service.ErrDevBoxAuthentication):
		return response.Unauthorized(c)
	default:
		return response.InternalErrorErr(c, err)
	}
}
