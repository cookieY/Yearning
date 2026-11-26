// osc_topology.go implements OSC SSRF/topology preflight before launching in-process gh-ost:
// pins target host to a single validated IP, verifies replication status over a guarded connection,
// and requires any discovered replica primary to pass addrguard AND match an enabled registered MySQL datasource.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
)

const (
	failureOSCTargetAddressForbidden = "OSC_TARGET_ADDRESS_FORBIDDEN"
	failureOSCTopologyCheckFailed    = "OSC_TOPOLOGY_CHECK_FAILED"
	failureOSCReplicationNotRunning  = "OSC_REPLICATION_NOT_RUNNING"
	failureOSCMasterAddressForbidden = "OSC_MASTER_ADDRESS_FORBIDDEN"
	failureOSCMasterNotRegistered    = "OSC_MASTER_NOT_REGISTERED"
)

func (s *Services) oscResolver() addrguard.Resolver {
	if s.OscResolver != nil {
		return s.OscResolver
	}
	return addrguard.DefaultResolver
}

// preflightOscTopologyAndPin resolves the stage datasource host to a validated IP, probes MySQL replication
// topology for gh-ost over an addrguard-protected connection, and locks AssumeMasterHostname so gh-ost never
// connects to an untrusted Master_Host from SHOW SLAVE STATUS.
func (s *Services) preflightOscTopologyAndPin(
	ctx context.Context,
	in TaskInput,
	at *persistence.ExecutionAttemptModel,
	tool osc.Tool,
	actor Actor,
) (pinnedIP string, assumeMaster string, failFacts *resultFacts, err error) {
	stage, err := s.loadStageByID(ctx, s.DB, in.StageID)
	if err != nil {
		return "", "", nil, err
	}
	ds, err := s.loadDatasource(ctx, s.DB, stage.DatasourceID)
	if err != nil {
		return "", "", nil, err
	}

	targetIP, perr := addrguard.ResolvePinned(ctx, addrguard.PurposeOSC, ds.Host, ds.Port, ptr.Deref(ds.DatabaseName), s.oscResolver())
	if perr != nil {
		s.auditOscPreflightDenied(ctx, at, in, actor, "osc.target_address_forbidden", map[string]any{
			"datasource_id": ds.ID, "host": ds.Host, "port": ds.Port, "reason": perr.Error(),
		})
		return "", "", oscPreflightFailFacts(failureOSCTargetAddressForbidden, map[string]any{
			"reason": perr.Error(),
		}), nil
	}
	pinned := targetIP.String()
	assume := net.JoinHostPort(pinned, strconv.Itoa(ds.Port))

	if tool != osc.ToolGhOst {
		return pinned, assume, nil, nil
	}

	var status *database.MySQLReplicaStatus
	if s.OscReplicaStatusProbe != nil {
		status, err = s.OscReplicaStatusProbe(ctx, nil)
	} else {
		var target *stageTarget
		var cleanup func()
		target, cleanup, err = s.openStageTarget(ctx, in)
		if err == nil {
			defer cleanup()
			status, err = database.ReadMySQLReplicaStatus(ctx, target.Adapter, target.Pool)
		}
	}
	if err != nil {
		s.auditOscPreflightDenied(ctx, at, in, actor, "osc.topology_check_failed", map[string]any{
			"datasource_id":   ds.ID,
			"privilege_error": errors.Is(err, database.ErrReplicaStatusPrivilege),
			"reason":          err.Error(),
		})
		return "", "", oscPreflightFailFacts(failureOSCTopologyCheckFailed, map[string]any{
			"reason": "unable to query replication status",
		}), nil
	}
	if status == nil || !status.IsReplica {
		return pinned, assume, nil, nil
	}
	if !status.ReplicationRunning {
		s.auditOscPreflightDenied(ctx, at, in, actor, "osc.replication_not_running", map[string]any{
			"datasource_id": ds.ID, "master_host": status.MasterHost, "master_port": status.MasterPort,
		})
		return "", "", oscPreflightFailFacts(failureOSCReplicationNotRunning, map[string]any{
			"master_endpoint": net.JoinHostPort(strings.Trim(strings.TrimSpace(status.MasterHost), "[]"), strconv.Itoa(status.MasterPort)),
		}), nil
	}

	masterIP, merr := addrguard.ResolvePinned(ctx, addrguard.PurposeOSC, status.MasterHost, status.MasterPort, "", s.oscResolver())
	if merr != nil {
		s.auditOscPreflightDenied(ctx, at, in, actor, "osc.master_address_forbidden", map[string]any{
			"datasource_id": ds.ID, "master_host": status.MasterHost, "master_port": status.MasterPort, "reason": merr.Error(),
		})
		return "", "", oscPreflightFailFacts(failureOSCMasterAddressForbidden, map[string]any{
			"reason": merr.Error(),
		}), nil
	}

	registered, rerr := s.hasRegisteredMySQLDatasource(ctx, status.MasterHost, masterIP, status.MasterPort)
	if rerr != nil {
		return "", "", nil, rerr
	}
	if !registered {
		endpoint := net.JoinHostPort(strings.Trim(strings.TrimSpace(status.MasterHost), "[]"), strconv.Itoa(status.MasterPort))
		s.auditOscPreflightDenied(ctx, at, in, actor, "osc.master_not_registered", map[string]any{
			"datasource_id": ds.ID,
			"master_host":   status.MasterHost,
			"master_port":   status.MasterPort,
			"master_ip":     masterIP.String(),
		})
		return "", "", oscPreflightFailFacts(failureOSCMasterNotRegistered, map[string]any{
			"master_endpoint": endpoint,
		}), nil
	}

	return pinned, net.JoinHostPort(masterIP.String(), strconv.Itoa(status.MasterPort)), nil, nil
}

func (s *Services) hasRegisteredMySQLDatasource(ctx context.Context, rawHost string, masterIP net.IP, port int) (bool, error) {
	var rows []struct {
		Host string `gorm:"column:host"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select host from datasources
		where enabled = true and compatibility_mode = 'mysql' and port = ?`, port).
		Scan(&rows).Error; err != nil {
		return false, err
	}
	trimmedMaster := strings.TrimSpace(rawHost)
	resolver := s.oscResolver()
	for _, row := range rows {
		candidate := strings.TrimSpace(row.Host)
		if candidate == "" {
			continue
		}
		if strings.EqualFold(candidate, trimmedMaster) {
			return true, nil
		}
		normCandidate := strings.Trim(candidate, "[]")
		if i := strings.IndexByte(normCandidate, '%'); i >= 0 {
			normCandidate = normCandidate[:i]
		}
		if ip := net.ParseIP(normCandidate); ip != nil {
			if ip.Equal(masterIP) {
				return true, nil
			}
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := resolver(lookupCtx, candidate)
		cancel()
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if ip.Equal(masterIP) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Services) auditOscPreflightDenied(
	ctx context.Context,
	at *persistence.ExecutionAttemptModel,
	in TaskInput,
	actor Actor,
	eventType string,
	extra map[string]any,
) {
	if s.Recorder == nil {
		return
	}
	meta := map[string]any{
		"order_id":   in.OrderID,
		"stage_id":   in.StageID,
		"attempt_id": at.ID,
	}
	for k, v := range extra {
		meta[k] = v
	}
	var actorID *string
	actorKind := "worker"
	if _, err := uuid.Parse(actor.UserID); err == nil {
		actorKind = "user"
		actorID = ptr.To(actor.UserID)
	}
	var actorUsername *string
	if actor.Username != "" {
		actorUsername = ptr.To(actor.Username)
	}
	_ = s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType:     eventType,
		ActorKind:     actorKind,
		ActorID:       actorID,
		ActorUsername: actorUsername,
		ResourceType:  "execution_attempt",
		ResourceID:    ptr.To(at.ID),
		Action:        "preflight_osc_topology",
		Outcome:       "denied",
		Metadata:      meta,
	})
}

func oscPreflightFailFacts(failureName string, extra map[string]any) *resultFacts {
	now := nowUTC()
	code := int(domain.CodePreflightFailed)
	payload := map[string]any{"failure_name": failureName}
	for k, v := range extra {
		payload[k] = v
	}
	detail, _ := json.Marshal(payload)
	return &resultFacts{
		Terminal:    domain.AttemptFailed,
		Boundary:    domain.BoundaryNotStarted,
		ReasonCode:  code,
		FailureName: failureName,
		Statements: []stmtFact{{
			Ordinal:     1,
			State:       domain.StmtFailed,
			ErrCode:     &code,
			FailureName: &failureName,
			Detail:      detail,
			StartedAt:   &now,
			FinishedAt:  &now,
		}},
	}
}

func rewriteArgvHost(argv []string, pinnedIP string) []string {
	if pinnedIP == "" {
		return argv
	}
	out := make([]string, 0, len(argv))
	replaced := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if strings.HasPrefix(arg, "--host=") {
			out = append(out, "--host="+pinnedIP)
			replaced = true
			continue
		}
		if arg == "--host" && i+1 < len(argv) {
			out = append(out, "--host", pinnedIP)
			i++
			replaced = true
			continue
		}
		out = append(out, arg)
	}
	if !replaced {
		out = append(out, "--host="+pinnedIP)
	}
	return out
}
