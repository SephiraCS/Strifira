package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestDevBoxHandlerRejectsInvalidPathIDs(t *testing.T) {
	handler := NewDevBoxHandler(nil)
	for _, test := range []struct {
		name  string
		param string
		call  echo.HandlerFunc
	}{
		{name: "update", param: "boxId", call: handler.Update},
		{name: "rotate", param: "boxId", call: handler.RotateToken},
		{name: "revoke", param: "boxId", call: handler.Revoke},
		{name: "queue", param: "boxId", call: handler.CreateJob},
		{name: "get job", param: "jobId", call: handler.GetJob},
		{name: "cancel job", param: "jobId", call: handler.CancelJob},
		{name: "job logs", param: "jobId", call: handler.JobLogs},
		{name: "start", param: "jobId", call: handler.Start},
		{name: "renew", param: "jobId", call: handler.Renew},
		{name: "append log", param: "jobId", call: handler.AppendLog},
		{name: "complete", param: "jobId", call: handler.Complete},
	} {
		for _, value := range []string{"bad-id", uuid.Nil.String()} {
			t.Run(test.name+"/"+value, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c := devBoxHandlerContext(t, http.MethodPost, "/", `{}`, recorder)
				c.SetParamNames(test.param)
				c.SetParamValues(value)

				require.NoError(t, devBoxAuthenticatedHandler(test.call)(c))
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Contains(t, recorder.Body.String(), "BAD_REQUEST")
			})
		}
	}
}

func TestDevBoxHandlerRejectsInvalidBodiesBeforeCallingService(t *testing.T) {
	handler := NewDevBoxHandler(nil)
	issueID := uuid.NewString()
	for _, test := range []struct {
		name string
		body string
		call echo.HandlerFunc
	}{
		{name: "malformed JSON", body: `{`, call: handler.Create},
		{name: "missing name", body: `{}`, call: handler.Create},
		{name: "invalid update", body: `{"name":""}`, call: handler.Update},
		{name: "invalid issue", body: `{"issue_id":"invalid","provider":"codex"}`, call: handler.CreateJob},
		{name: "unsupported provider", body: fmt.Sprintf(`{"issue_id":%q,"provider":"custom"}`, issueID), call: handler.CreateJob},
		{name: "invalid test argv", body: fmt.Sprintf(`{"issue_id":%q,"provider":"codex","test_command":[""]}`, issueID), call: handler.CreateJob},
		{name: "incomplete heartbeat", body: `{}`, call: handler.Heartbeat},
		{name: "missing start lease", body: `{}`, call: handler.Start},
		{name: "missing renewal lease", body: `{}`, call: handler.Renew},
		{name: "zero log sequence", body: `{"lease_token":"lease","sequence":0,"stream":"stdout","content":"test"}`, call: handler.AppendLog},
		{name: "missing completion lease", body: `{"result":{"status":"succeeded"}}`, call: handler.Complete},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodPost, "/", test.body, recorder)

			require.NoError(t, devBoxAuthenticatedHandler(test.call)(c))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			var result dto.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Contains(t, []string{"BAD_REQUEST", "VALIDATION_ERROR"}, result.Error.Code)
		})
	}
}

func TestDevBoxHandlerCapsCompleteStreamedBodiesIncludingTrailingBytes(t *testing.T) {
	handler := NewDevBoxHandler(nil)
	for _, test := range []struct {
		name  string
		limit int
		call  echo.HandlerFunc
	}{
		{name: "user request", limit: 128 << 10, call: handler.Create},
		{name: "heartbeat", limit: 8 << 10, call: handler.Heartbeat},
		{name: "log append", limit: 70 << 10, call: handler.AppendLog},
		{name: "completion", limit: dto.DevBoxCompleteBodyLimit, call: handler.Complete},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodPost, "/", `{}`+strings.Repeat(" ", test.limit), recorder)
			c.Request().ContentLength = -1

			require.NoError(t, devBoxAuthenticatedHandler(test.call)(c))
			require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
			require.Contains(t, recorder.Body.String(), "BODY_TOO_LARGE")
		})
	}
}

func TestDevBoxCompletionPreservesMaximumEscapedResult(t *testing.T) {
	for _, character := range []string{"<", "\x01", "\u2028"} {
		t.Run(fmt.Sprintf("escaped %U", []rune(character)[0]), func(t *testing.T) {
			fill := func(maxBytes int) string {
				return strings.Repeat(character, maxBytes/len(character)) + strings.Repeat("<", maxBytes%len(character))
			}
			metadata := func(count int) []string {
				values := make([]string, count)
				for i := range count {
					values[i] = fill(4096)
				}
				return values
			}
			box, jobID := devBoxTestWorker(), uuid.New()
			box.TokenHash = strings.Repeat("b", 64)
			store := &devBoxCompletionStoreFake{job: &domain.DevBoxJob{
				ID: jobID, BoxID: box.ID, Status: domain.DevBoxJobRunning,
				WorkingBranch: "ku/ku-27-12345678", TestCommand: json.RawMessage(`[]`),
			}}
			request := dto.DevBoxCompleteRequest{
				LeaseToken: "kul_" + strings.Repeat("a", 64),
				Result: domain.DevBoxJobResult{
					Status: domain.DevBoxJobFailed, TestStatus: "not_run",
					Summary: fill(8192), Patch: fill(service.DevBoxMaxPatchBytes),
					ChangedFiles: metadata(500), TestsRun: metadata(100), RiskNotes: metadata(100),
					Branch: store.job.WorkingBranch,
				},
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			require.Greater(t, len(body), 2<<20)
			require.LessOrEqual(t, len(body), dto.DevBoxCompleteBodyLimit)
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodPost, "/", string(body), recorder)
			c.SetParamNames("jobId")
			c.SetParamValues(jobID.String())
			c.Request().ContentLength = -1
			handler := NewDevBoxHandler(service.NewDevBoxService(store, nil, nil, nil))
			authenticated := middleware.DevBoxWorkerAuth(&devBoxHandlerAuthenticator{box: box})(handler.Complete)

			require.NoError(t, authenticated(c))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.NotNil(t, store.completed)
			require.Equal(t, request.Result, *store.completed)
		})
	}
}

func TestDevBoxCompletionEncodedBodyBoundary(t *testing.T) {
	request := dto.DevBoxCompleteRequest{
		LeaseToken: "kul_" + strings.Repeat("a", 64),
		Result:     domain.DevBoxJobResult{Status: domain.DevBoxJobFailed, TestStatus: "not_run"},
	}
	body, err := json.Marshal(request)
	require.NoError(t, err)
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("limit plus %d", extra), func(t *testing.T) {
			fake := &devBoxHandlerServiceFake{job: &domain.DevBoxJob{ID: uuid.New()}}
			handler := NewDevBoxHandler(fake)
			recorder := httptest.NewRecorder()
			padded := string(body) + strings.Repeat(" ", dto.DevBoxCompleteBodyLimit-len(body)+extra)
			c := devBoxHandlerContext(t, http.MethodPost, "/", padded, recorder)
			c.Request().ContentLength = -1

			require.NoError(t, devBoxAuthenticatedHandler(handler.Complete)(c))
			if extra == 0 {
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, request, fake.completeRequest)
			} else {
				require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
				require.Contains(t, recorder.Body.String(), "BODY_TOO_LARGE")
				require.Nil(t, fake.workerBox)
			}
		})
	}
}

func TestDevBoxWorkerHandlersRequireAuthenticatedBox(t *testing.T) {
	handler := NewDevBoxHandler(nil)
	for _, test := range []struct {
		name string
		call echo.HandlerFunc
	}{
		{name: "heartbeat", call: handler.Heartbeat},
		{name: "claim", call: handler.Claim},
		{name: "start", call: handler.Start},
		{name: "renew", call: handler.Renew},
		{name: "append log", call: handler.AppendLog},
		{name: "complete", call: handler.Complete},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodPost, "/", `{}`, recorder)

			require.NoError(t, test.call(c))
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
		})
	}
}

func TestDevBoxCreateJobUsesAuthenticatedWorkspaceAndCreator(t *testing.T) {
	workspaceID, userID, boxID, issueID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	job := &domain.DevBoxJob{ID: uuid.New(), WorkspaceID: workspaceID, BoxID: boxID, RequestedByUserID: userID, LeaseHash: "private lease hash"}
	fake := &devBoxHandlerServiceFake{job: job}
	handler := NewDevBoxHandler(fake)
	requestBody := fmt.Sprintf(`{"issue_id":%q,"provider":"codex","test_command":["go","test","./..."],"workspace_id":%q,"requested_by_user_id":%q}`, issueID, uuid.New(), uuid.New())
	recorder := httptest.NewRecorder()
	c := devBoxHandlerContext(t, http.MethodPost, "/", requestBody, recorder)
	c.Set("workspace", &domain.Workspace{ID: workspaceID})
	c.Set(string(middleware.UserIDKey), userID)
	c.SetParamNames("boxId")
	c.SetParamValues(boxID.String())

	require.NoError(t, handler.CreateJob(c))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, workspaceID, fake.workspaceID)
	require.Equal(t, userID, fake.userID)
	require.Equal(t, boxID, fake.boxID)
	require.Equal(t, issueID.String(), fake.jobRequest.IssueID)
	require.Equal(t, []string{"go", "test", "./..."}, fake.jobRequest.TestCommand)
	require.NotContains(t, recorder.Body.String(), "private lease hash")
	require.NotContains(t, recorder.Body.String(), "lease_hash")
}

func TestDevBoxWorkerClaimUsesAuthenticatedBoxAndReturnsNoContentWhenIdle(t *testing.T) {
	fake := &devBoxHandlerServiceFake{}
	handler := NewDevBoxHandler(fake)
	recorder := httptest.NewRecorder()
	c := devBoxHandlerContext(t, http.MethodPost, "/?box_id="+uuid.NewString(), "", recorder)
	box := devBoxTestWorker()
	authenticated := middleware.DevBoxWorkerAuth(&devBoxHandlerAuthenticator{box: box})(handler.Claim)

	require.NoError(t, authenticated(c))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Empty(t, recorder.Body.String())
	require.Same(t, box, fake.workerBox)

	fake.claim = &dto.DevBoxClaimResponse{Job: &domain.DevBoxJob{ID: uuid.New()}, LeaseToken: "lease-secret"}
	recorder = httptest.NewRecorder()
	c = devBoxHandlerContext(t, http.MethodPost, "/", `{}`, recorder)
	require.NoError(t, authenticated(c))
	require.Equal(t, http.StatusOK, recorder.Code)
	var result dto.DevBoxClaimResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, fake.claim.Job.ID, result.Job.ID)
	require.Equal(t, "lease-secret", result.LeaseToken)
}

func TestDevBoxWorkerStartPassesLeaseAndAuthenticatedBox(t *testing.T) {
	jobID := uuid.New()
	fake := &devBoxHandlerServiceFake{}
	handler := NewDevBoxHandler(fake)
	recorder := httptest.NewRecorder()
	c := devBoxHandlerContext(t, http.MethodPost, "/", `{"lease_token":"job-lease","box_id":"ignored","workspace_id":"ignored"}`, recorder)
	c.SetParamNames("jobId")
	c.SetParamValues(jobID.String())
	box := devBoxTestWorker()
	authenticated := middleware.DevBoxWorkerAuth(&devBoxHandlerAuthenticator{box: box})(handler.Start)

	require.NoError(t, authenticated(c))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Same(t, box, fake.workerBox)
	require.Equal(t, jobID, fake.jobID)
	require.Equal(t, "job-lease", fake.leaseRequest.LeaseToken)
}

func TestDevBoxJobListDefaultsAndValidation(t *testing.T) {
	fake := &devBoxHandlerServiceFake{total: 101}
	handler := NewDevBoxHandler(fake)
	recorder := httptest.NewRecorder()
	c := devBoxHandlerContext(t, http.MethodGet, "/", "", recorder)
	workspaceID := middleware.GetWorkspace(c).ID
	userID := middleware.GetUserID(c)

	require.NoError(t, handler.ListJobs(c))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, workspaceID, fake.workspaceID)
	require.Equal(t, userID, fake.userID)
	require.Equal(t, 1, fake.listParams.Page)
	require.Equal(t, 50, fake.listParams.PerPage)
	var result dto.ListResponse[domain.DevBoxJob]
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.NotNil(t, result.Data)
	require.True(t, result.HasMore)
	require.Equal(t, 101, result.TotalCount)

	for _, query := range []string{"?page=-1", "?per_page=101", "?page=invalid", "?issue_id=invalid", "?box_id=invalid"} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodGet, "/"+query, "", recorder)
			require.NoError(t, NewDevBoxHandler(nil).ListJobs(c))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestDevBoxErrorsUseStatusCodesAndSanitizeMessages(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: service.ErrDevBoxNotFound, status: http.StatusNotFound, code: "NOT_FOUND"},
		{name: "conflict", err: service.ErrDevBoxConflict, status: http.StatusConflict, code: "DEV_BOX_CONFLICT"},
		{name: "stale lease", err: service.ErrDevBoxLeaseLost, status: http.StatusConflict, code: "LEASE_LOST"},
		{name: "invalid input", err: service.ErrInvalidDevBoxInput, status: http.StatusBadRequest, code: "BAD_REQUEST"},
		{name: "forbidden", err: service.ErrDevBoxForbidden, status: http.StatusForbidden, code: "FORBIDDEN"},
		{name: "authentication", err: service.ErrDevBoxAuthentication, status: http.StatusUnauthorized, code: "UNAUTHORIZED"},
		{name: "internal", err: errors.New("query failed"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c := devBoxHandlerContext(t, http.MethodPost, "/", "", recorder)

			require.NoError(t, devBoxError(c, fmt.Errorf("%w: database password kub_private-token", test.err)))
			require.Equal(t, test.status, recorder.Code)
			var result dto.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, test.code, result.Error.Code)
			require.NotContains(t, result.Error.Message, "database password")
			require.NotContains(t, result.Error.Message, "kub_")
		})
	}
}

type devBoxHandlerServiceFake struct {
	devBoxService
	job             *domain.DevBoxJob
	claim           *dto.DevBoxClaimResponse
	total           int
	workspaceID     uuid.UUID
	userID          uuid.UUID
	boxID           uuid.UUID
	jobID           uuid.UUID
	workerBox       *domain.DevBox
	jobRequest      dto.CreateDevBoxJobRequest
	leaseRequest    dto.DevBoxLeaseRequest
	listParams      dto.DevBoxJobListParams
	completeRequest dto.DevBoxCompleteRequest
}

func (f *devBoxHandlerServiceFake) CreateJob(_ context.Context, workspaceID, boxID, userID uuid.UUID, request dto.CreateDevBoxJobRequest) (*domain.DevBoxJob, error) {
	f.workspaceID, f.boxID, f.userID, f.jobRequest = workspaceID, boxID, userID, request
	return f.job, nil
}

func (f *devBoxHandlerServiceFake) Claim(_ context.Context, box *domain.DevBox) (*dto.DevBoxClaimResponse, error) {
	f.workerBox = box
	return f.claim, nil
}

func (f *devBoxHandlerServiceFake) Start(_ context.Context, box *domain.DevBox, jobID uuid.UUID, request dto.DevBoxLeaseRequest) (*dto.DevBoxLeaseResponse, error) {
	f.workerBox, f.jobID, f.leaseRequest = box, jobID, request
	return &dto.DevBoxLeaseResponse{Status: domain.DevBoxJobRunning}, nil
}

func (f *devBoxHandlerServiceFake) Complete(_ context.Context, box *domain.DevBox, jobID uuid.UUID, request dto.DevBoxCompleteRequest) (*domain.DevBoxJob, error) {
	f.workerBox, f.jobID, f.completeRequest = box, jobID, request
	return f.job, nil
}

func (f *devBoxHandlerServiceFake) ListJobs(_ context.Context, workspaceID, userID uuid.UUID, params dto.DevBoxJobListParams) ([]domain.DevBoxJob, int, error) {
	f.workspaceID, f.userID, f.listParams = workspaceID, userID, params
	return nil, f.total, nil
}

type devBoxCompletionStoreFake struct {
	repository.DevBoxStore
	job       *domain.DevBoxJob
	completed *domain.DevBoxJobResult
}

func (f *devBoxCompletionStoreFake) GetWorkerJob(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.DevBoxJob, error) {
	return f.job, nil
}

func (f *devBoxCompletionStoreFake) CompleteJob(_ context.Context, _, _ uuid.UUID, _, _ string, result *domain.DevBoxJobResult) (*domain.DevBoxJob, error) {
	f.completed = result
	f.job.Status = result.Status
	return f.job, nil
}

type devBoxHandlerAuthenticator struct {
	box *domain.DevBox
}

func (f *devBoxHandlerAuthenticator) AuthenticateWorker(context.Context, string) (*domain.DevBox, error) {
	return f.box, nil
}

func devBoxTestWorker() *domain.DevBox {
	return &domain.DevBox{ID: uuid.New(), WorkspaceID: uuid.New(), CreatedByUserID: uuid.New(), Enabled: true}
}

func devBoxAuthenticatedHandler(handler echo.HandlerFunc) echo.HandlerFunc {
	return middleware.DevBoxWorkerAuth(&devBoxHandlerAuthenticator{box: devBoxTestWorker()})(handler)
}

func devBoxHandlerContext(t *testing.T, method, target, body string, recorder *httptest.ResponseRecorder) echo.Context {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(t.Context())
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer kub_test-token")
	c := echo.New().NewContext(req, recorder)
	c.Set("workspace", &domain.Workspace{ID: uuid.New()})
	c.Set(string(middleware.UserIDKey), uuid.New())
	c.SetParamNames("boxId", "jobId")
	c.SetParamValues(uuid.NewString(), uuid.NewString())
	return c
}
