// mock.go implements the OpenAPI Mock Server (B1 deliverable).
//
// Purpose: contract-level Mock for independent frontend development. It implements no business logic --
// response behavior is driven by scenario kind in testdata/development/mock-scenarios.json,
// and route validity is driven by api/openapi/yearning-v4.yaml. Both scenario file and OpenAPI spec are read-only inputs.
// Control endpoints exist only in the mock process and are never registered in serve mode.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// APIV4Prefix is the base path declared in OpenAPI servers.url.
const APIV4Prefix = "/api/v4"

// mockScenario corresponds to testdata/development/mock-scenarios.schema.json.
type mockScenario struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Description string         `json:"description"`
	Expectation map[string]any `json:"expectation"`
}

var validScenarioKinds = map[string]bool{
	"success":              true,
	"business_error":       true,
	"transport_error":      true,
	"state_progression":    true,
	"event_disorder":       true,
	"authorization_denial": true,
}

type scenariosFile struct {
	ContractVersion int            `json:"contract_version"`
	OpenAPISource   string         `json:"openapi_source"`
	Scenarios       []mockScenario `json:"scenarios"`
}

// MockEngine is the scenario-driven contract Mock engine.
type MockEngine struct {
	table     *RouteTable
	scenarios map[string]mockScenario
	order     []string
	active    atomic.Value // string scenario ID
	logger    *zap.Logger
}

// NewMockEngine loads the OpenAPI route table and scenario file, refusing to start on any error.
func NewMockEngine(openapiPath, scenariosPath string, logger *zap.Logger) (*MockEngine, error) {
	table, err := LoadRouteTable(openapiPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(scenariosPath)
	if err != nil {
		return nil, err
	}
	var file scenariosFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.ContractVersion != 1 {
		return nil, errors.New("unsupported mock scenarios contract_version")
	}
	engine := &MockEngine{
		table:     table,
		scenarios: map[string]mockScenario{},
		logger:    logger,
	}
	for _, scenario := range file.Scenarios {
		if !validScenarioKinds[scenario.Kind] {
			return nil, errors.New("unknown scenario kind: " + scenario.Kind)
		}
		engine.scenarios[scenario.ID] = scenario
		engine.order = append(engine.order, scenario.ID)
	}
	if len(engine.scenarios) == 0 {
		return nil, errors.New("mock scenarios file has no scenarios")
	}
	engine.active.Store(engine.order[0])
	return engine, nil
}

// RegisterRoutes mounts mock control endpoints and the business fallback handler.
// Control endpoints /api/v4/_mock/* start with an underscore, explicitly isolated from OpenAPI business paths.
func (e *MockEngine) RegisterRoutes(engine *gin.Engine) {
	engine.GET(APIV4Prefix+"/_mock/scenario", e.handleGetScenario)
	engine.POST(APIV4Prefix+"/_mock/scenario", e.handleSetScenario)
	engine.NoRoute(e.handleBusiness)
}

func (e *MockEngine) handleGetScenario(c *gin.Context) {
	WriteSuccess(c, gin.H{"scenario_id": e.activeScenario().ID, "kind": e.activeScenario().Kind})
}

func (e *MockEngine) handleSetScenario(c *gin.Context) {
	var body struct {
		ScenarioID string `json:"scenario_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteBusinessError(c, &BusinessError{
			Code:    ErrCodeValidationFailed,
			Name:    "VALIDATION_FAILED",
			Message: "Request body must be valid JSON containing scenario_id.",
		})
		return
	}
	if _, ok := e.scenarios[body.ScenarioID]; !ok {
		WriteBusinessError(c, &BusinessError{
			Code:    ErrCodeResourceNotFound,
			Name:    "RESOURCE_NOT_FOUND",
			Message: "Scenario not found.",
		})
		return
	}
	e.active.Store(body.ScenarioID)
	WriteSuccess(c, gin.H{"scenario_id": body.ScenarioID})
}

// handleBusiness is the unified Mock handler for all contract business paths.
func (e *MockEngine) handleBusiness(c *gin.Context) {
	contractPath := strings.TrimPrefix(c.Request.URL.Path, APIV4Prefix)
	kind, _ := e.table.Match(contractPath, c.Request.Method)
	switch kind {
	case Matched:
		e.renderScenario(c)
	case MethodNotAllowed:
		MethodNotAllowedHandler(c)
	default:
		NotFoundHandler(c)
	}
}

// renderScenario renders the response according to the active scenario kind.
// Mapping rules (B1 platform layer, carrying no business data):
//
//	success/state_progression/event_disorder -> 200 err_code=0
//	business_error -> 200 non-zero err_code (expectation.err_code preferred, defaults to 1001)
//	transport_error -> real HTTP error Problem Details (expectation.http_status preferred)
//	authorization_denial -> 403 Problem Details, without disclosing resource existence
func (e *MockEngine) renderScenario(c *gin.Context) {
	scenario := e.activeScenario()
	switch scenario.Kind {
	case "business_error":
		code := ErrCodeValidationFailed
		if raw, ok := expectationInt(scenario, "err_code"); ok {
			code = raw
		}
		WriteBusinessError(c, &BusinessError{
			Code:    code,
			Name:    "MOCK_SCENARIO",
			Message: "Mock scenario business outcome: " + scenario.Description,
		})
	case "transport_error":
		status := http.StatusServiceUnavailable
		if raw, ok := expectationInt(scenario, "http_status"); ok {
			status = raw
		}
		WriteProblem(c, status, "mock transport error scenario: "+scenario.ID)
	case "authorization_denial":
		WriteProblem(c, http.StatusForbidden, "authorization denied")
	default:
		WriteSuccess(c, nil)
	}
}

func (e *MockEngine) activeScenario() mockScenario {
	id, _ := e.active.Load().(string)
	scenario, ok := e.scenarios[id]
	if !ok {
		scenario = e.scenarios[e.order[0]]
	}
	return scenario
}

func expectationInt(scenario mockScenario, key string) (int, bool) {
	raw, ok := scenario.Expectation[key].(float64)
	if !ok {
		return 0, false
	}
	return int(raw), true
}

// NewMockRouter assembles the mock process: shares core middleware and platform endpoints with serve,
// while delegating business paths to MockEngine. Mock mode connects to neither database nor Key Provider.
func NewMockRouter(engine *MockEngine, logger *zap.Logger, readiness *Readiness) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.Use(RequestIDMiddleware())
	router.Use(RecoveryMiddleware(logger))
	router.Use(AccessLogMiddleware(logger))
	router.NoMethod(MethodNotAllowedHandler)

	router.GET("/api/v4/health/live", readiness.HandleLive)
	router.GET("/api/v4/health/ready", readiness.HandleReady)
	router.GET("/api/v4/version", HandleVersion)

	engine.RegisterRoutes(router)
	return router
}
