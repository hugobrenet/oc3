package serverhandlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
)

// maxOrchestrationTargets bounds the targets of a step, a filterset matching the
// whole fleet being more likely a mistake than a plan.
const maxOrchestrationTargets = 1000

// orchestrationTarget is a target of a step, resolved: a service, an instance (a
// service and a node), a node or a cluster, and its name.
type orchestrationTarget struct {
	nodeID    string
	svcID     string
	clusterID string
	label     string
}

// orchestrationActionAllowed tells whether the action queue accepts the action
// on that kind of target (see PUT /actions).
func orchestrationActionAllowed(kind server.OrchestrationTargetKind, action string) bool {
	switch kind {
	case server.OrchestrationTargetKindService:
		return serviceActions[action]
	case server.OrchestrationTargetKindInstance:
		return instanceActions[action]
	case server.OrchestrationTargetKindNode:
		return nodeActions[action]
	case server.OrchestrationTargetKindCluster:
		_, ok := clusterActionWords[action]
		return ok
	}
	return false
}

// validateOrchestration checks a plan as a whole, and names the first step that
// is not valid.
func validateOrchestration(in server.OrchestrationInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("name is required")
	}
	if len(in.Steps) == 0 {
		return errors.New("an orchestration needs at least one step")
	}
	for i, step := range in.Steps {
		if err := validateOrchestrationStep(in.Steps, i, step); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func validateOrchestrationStep(steps []server.OrchestrationStep, i int, step server.OrchestrationStep) error {
	if !step.Kind.Valid() {
		return fmt.Errorf("unknown kind %q", step.Kind)
	}
	if !orchestrationActionAllowed(step.Kind, step.Action) {
		return fmt.Errorf("action %q is not accepted on a %s", step.Action, step.Kind)
	}
	if step.Rid != nil && *step.Rid != "" {
		if step.Kind != server.OrchestrationTargetKindInstance {
			return errors.New("rid applies to instances only")
		}
		if !ridPattern.MatchString(*step.Rid) {
			return fmt.Errorf("invalid rid %q", *step.Rid)
		}
	}
	switch step.Pace {
	case server.All, server.One:
	case server.Batch:
		if step.BatchSize == nil || *step.BatchSize < 1 {
			return errors.New("pace batch needs a batch_size of 1 or more")
		}
	default:
		return fmt.Errorf("unknown pace %q", step.Pace)
	}
	if step.Timeout != nil && *step.Timeout < 0 {
		return errors.New("timeout must be 0 or more")
	}
	if th := step.FailureThreshold; th != nil {
		switch th.Unit {
		case server.Count:
			if th.Value < 1 {
				return errors.New("failure_threshold must be 1 or more")
			}
		case server.Percent:
			if th.Value < 1 || th.Value > 100 {
				return errors.New("failure_threshold must be a percentage from 1 to 100")
			}
		default:
			return fmt.Errorf("unknown failure_threshold unit %q", th.Unit)
		}
	}
	switch step.Targets.Mode {
	case server.Pick:
		if step.Targets.Ids == nil || len(*step.Targets.Ids) == 0 {
			return errors.New("no target picked")
		}
		if len(*step.Targets.Ids) > maxOrchestrationTargets {
			return fmt.Errorf("more than %d targets", maxOrchestrationTargets)
		}
		if step.Kind == server.OrchestrationTargetKindInstance {
			for _, id := range *step.Targets.Ids {
				if svcID, nodeID, ok := strings.Cut(id, "@"); !ok || svcID == "" || nodeID == "" {
					return fmt.Errorf("instance %q is not svc_id@node_id", id)
				}
			}
		}
	case server.Filterset:
		if step.Targets.FiltersetId == nil {
			return errors.New("no filterset chosen")
		}
	case server.SameAs:
		if step.Targets.Step == nil || *step.Targets.Step < 0 || *step.Targets.Step >= i {
			return errors.New("same_as names an earlier step")
		}
		if steps[*step.Targets.Step].Kind != step.Kind {
			return fmt.Errorf("same_as names step %d, whose targets are not %ss", *step.Targets.Step+1, step.Kind)
		}
	default:
		return fmt.Errorf("unknown targets mode %q", step.Targets.Mode)
	}
	return nil
}

// resolveOrchestrationTargets resolves the targets of every step, as a run
// starting now would: the filtersets matched, the targets of a same_as step
// copied from the step it names. A step whose targets cannot be resolved has a
// message, and no target.
func (a *Api) resolveOrchestrationTargets(ctx context.Context, steps []server.OrchestrationStep) ([][]orchestrationTarget, []string, error) {
	resolved := make([][]orchestrationTarget, len(steps))
	messages := make([]string, len(steps))
	for i, step := range steps {
		if step.Targets.Mode == server.SameAs && step.Targets.Step != nil {
			resolved[i] = resolved[*step.Targets.Step]
			continue
		}
		targets, msg, err := a.resolveStepTargets(ctx, step)
		if err != nil {
			return nil, nil, err
		}
		if msg == "" && len(targets) == 0 {
			msg = "no target"
		}
		if len(targets) > maxOrchestrationTargets {
			msg = fmt.Sprintf("%d targets, more than %d", len(targets), maxOrchestrationTargets)
			targets = nil
		}
		resolved[i], messages[i] = targets, msg
	}
	return resolved, messages, nil
}

func (a *Api) resolveStepTargets(ctx context.Context, step server.OrchestrationStep) ([]orchestrationTarget, string, error) {
	odb := a.ODB
	var svcIDs, nodeIDs, clusterIDs []string
	var instances []cdb.InstanceRef
	if step.Targets.Mode == server.Filterset {
		if step.Targets.FiltersetId == nil {
			return nil, "no filterset chosen", nil
		}
		fsetID := *step.Targets.FiltersetId
		found, _, err := odb.FiltersetByIDOrName(ctx, fmt.Sprint(fsetID))
		if err != nil {
			return nil, "", err
		}
		if found == 0 {
			return nil, fmt.Sprintf("filterset %d does not exist", fsetID), nil
		}
		needServices := step.Kind == server.OrchestrationTargetKindService || step.Kind == server.OrchestrationTargetKindInstance
		needNodes := step.Kind != server.OrchestrationTargetKindService
		if needServices {
			if svcIDs, err = odb.ResolveFilterset(ctx, fsetID, "svc_id"); err != nil {
				return nil, "", err
			}
		}
		if needNodes {
			if nodeIDs, err = odb.ResolveFilterset(ctx, fsetID, "node_id"); err != nil {
				return nil, "", err
			}
		}
		switch step.Kind {
		case server.OrchestrationTargetKindInstance:
			if instances, err = odb.InstancesOf(ctx, svcIDs, nodeIDs); err != nil {
				return nil, "", err
			}
		case server.OrchestrationTargetKindCluster:
			if clusterIDs, err = odb.ClustersOfNodes(ctx, nodeIDs); err != nil {
				return nil, "", err
			}
		}
	} else if step.Targets.Ids != nil {
		for _, id := range *step.Targets.Ids {
			switch step.Kind {
			case server.OrchestrationTargetKindService:
				svcIDs = append(svcIDs, id)
			case server.OrchestrationTargetKindNode:
				nodeIDs = append(nodeIDs, id)
			case server.OrchestrationTargetKindCluster:
				clusterIDs = append(clusterIDs, id)
			case server.OrchestrationTargetKindInstance:
				svcID, nodeID, _ := strings.Cut(id, "@")
				instances = append(instances, cdb.InstanceRef{SvcID: svcID, NodeID: nodeID})
			}
		}
	}

	// The names, for the run to show: a target unknown to the collector is kept,
	// named by its id, for the action queue to refuse it with its reason.
	names := func(table string, ids []string) (map[string]string, error) {
		m, err := odb.ObjectNames(ctx, table, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if m[id] == "" {
				m[id] = id
			}
		}
		return m, nil
	}
	var targets []orchestrationTarget
	seen := map[string]bool{}
	add := func(key string, t orchestrationTarget) {
		if !seen[key] {
			seen[key] = true
			targets = append(targets, t)
		}
	}
	switch step.Kind {
	case server.OrchestrationTargetKindService:
		m, err := names("services", svcIDs)
		if err != nil {
			return nil, "", err
		}
		for _, id := range svcIDs {
			add(id, orchestrationTarget{svcID: id, label: m[id]})
		}
	case server.OrchestrationTargetKindNode:
		m, err := names("nodes", nodeIDs)
		if err != nil {
			return nil, "", err
		}
		for _, id := range nodeIDs {
			add(id, orchestrationTarget{nodeID: id, label: m[id]})
		}
	case server.OrchestrationTargetKindCluster:
		m, err := names("clusters", clusterIDs)
		if err != nil {
			return nil, "", err
		}
		for _, id := range clusterIDs {
			add(id, orchestrationTarget{clusterID: id, label: m[id]})
		}
	case server.OrchestrationTargetKindInstance:
		var s, n []string
		for _, r := range instances {
			s, n = append(s, r.SvcID), append(n, r.NodeID)
		}
		svcNames, err := names("services", s)
		if err != nil {
			return nil, "", err
		}
		nodeNames, err := names("nodes", n)
		if err != nil {
			return nil, "", err
		}
		for _, r := range instances {
			add(r.SvcID+"@"+r.NodeID, orchestrationTarget{svcID: r.SvcID, nodeID: r.NodeID,
				label: svcNames[r.SvcID] + " @ " + nodeNames[r.NodeID]})
		}
	}
	return targets, "", nil
}

// queueOrchestrationTarget queues, or with a dry run actor only checks, the
// action of a step on one of its targets.
func (a *Api) queueOrchestrationTarget(act actionActor, log *slog.Logger, ctx context.Context, step server.OrchestrationStep, t orchestrationTarget) (*queuedAction, error) {
	e := actionEntry{Action: step.Action}
	switch step.Kind {
	case server.OrchestrationTargetKindService:
		e.SvcID = t.svcID
	case server.OrchestrationTargetKindInstance:
		e.SvcID, e.NodeID = t.svcID, t.nodeID
		if step.Rid != nil {
			e.Rid = *step.Rid
		}
	case server.OrchestrationTargetKindNode:
		e.NodeID = t.nodeID
	case server.OrchestrationTargetKindCluster:
		e.ClusterID = t.clusterID
	}
	return a.queueActionEntry(act, log, ctx, e)
}

func toOrchestration(o cdb.Orchestration) (server.Orchestration, error) {
	steps := []server.OrchestrationStep{}
	if err := json.Unmarshal([]byte(o.StepsJSON), &steps); err != nil {
		return server.Orchestration{}, err
	}
	return server.Orchestration{
		Id: o.ID, Name: o.Name, Description: o.Description, Steps: steps,
		Created: o.Created.Format(time.RFC3339), Updated: o.Updated.Format(time.RFC3339), UpdatedBy: o.UpdatedBy,
	}, nil
}

// GetOrchestrations handles GET /orchestrations.
func (a *Api) GetOrchestrations(c echo.Context, params server.GetOrchestrationsParams) error {
	if err := requireManager(c); err != nil {
		return err
	}
	return a.handleList(c, "GetOrchestrations", "orchestrationList", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetOrchestrationsList(ctx, p)
	})
}

// GetOrchestration handles GET /orchestrations/{orchestration_id}.
func (a *Api) GetOrchestration(c echo.Context, id server.InPathOrchestrationId) error {
	log := echolog.GetLogHandler(c, "GetOrchestration")
	if err := requireManager(c); err != nil {
		return err
	}
	return a.writeOrchestration(c, log, id)
}

func (a *Api) writeOrchestration(c echo.Context, log *slog.Logger, id int) error {
	row, err := a.ODB.OrchestrationByID(c.Request().Context(), id)
	if err != nil {
		log.Error("cannot read the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot read orchestration %d", id)
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "orchestration %d not found", id)
	}
	o, err := toOrchestration(*row)
	if err != nil {
		log.Error("cannot read the steps of the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot read orchestration %d", id)
	}
	return c.JSON(http.StatusOK, o)
}

// bindOrchestration reads and checks the plan of a request.
func bindOrchestration(c echo.Context) (server.OrchestrationInput, string, error) {
	var in server.OrchestrationInput
	if err := c.Bind(&in); err != nil {
		return in, "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := validateOrchestration(in); err != nil {
		return in, "", err
	}
	stepsJSON, err := json.Marshal(in.Steps)
	return in, string(stepsJSON), err
}

// PostOrchestration handles POST /orchestrations.
func (a *Api) PostOrchestration(c echo.Context) error {
	return a.saveOrchestration(c, 0)
}

// PutOrchestration handles PUT /orchestrations/{orchestration_id}.
func (a *Api) PutOrchestration(c echo.Context, id server.InPathOrchestrationId) error {
	return a.saveOrchestration(c, id)
}

// saveOrchestration creates the orchestration (id 0) or replaces it.
func (a *Api) saveOrchestration(c echo.Context, id int) error {
	log := echolog.GetLogHandler(c, "saveOrchestration")
	if err := requireManager(c); err != nil {
		return err
	}
	in, stepsJSON, err := bindOrchestration(c)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	odb := a.ODB
	taken, err := odb.OrchestrationNameTaken(ctx, in.Name, id)
	if err != nil {
		log.Error("cannot check the name", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot check the name")
	}
	if taken {
		return JSONProblemf(c, http.StatusConflict, "an orchestration is named %s already", in.Name)
	}
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	author, _ := c.Get(XUserEmail).(string)
	row := cdb.Orchestration{ID: id, Name: in.Name, Description: description, StepsJSON: stepsJSON, UpdatedBy: author}
	verb := "changed"
	if id == 0 {
		verb = "created"
		if id, err = odb.InsertOrchestration(ctx, row); err != nil {
			log.Error("cannot create the orchestration", logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot create the orchestration")
		}
	} else {
		found, err := odb.UpdateOrchestration(ctx, row)
		if err != nil {
			log.Error("cannot change the orchestration", "orchestration_id", id, logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot change orchestration %d", id)
		}
		if !found {
			return JSONProblemf(c, http.StatusNotFound, "orchestration %d not found", id)
		}
	}
	if err := odb.Log(ctx, cdb.LogEntry{
		Action: "orchestration." + strings.TrimSuffix(verb, "d"),
		User:   author,
		Fmt:    verb + " orchestration %(name)s",
		Dict:   map[string]any{"name": in.Name},
		Level:  "info",
	}); err != nil {
		log.Error("cannot write audit log", logkey.Error, err)
	}
	if err := odb.Session.NotifyChanges(ctx); err != nil {
		log.Debug("cannot notify changes", logkey.Error, err)
	}
	return a.writeOrchestration(c, log, id)
}

// DeleteOrchestration handles DELETE /orchestrations/{orchestration_id}.
func (a *Api) DeleteOrchestration(c echo.Context, id server.InPathOrchestrationId) error {
	log := echolog.GetLogHandler(c, "DeleteOrchestration")
	if err := requireManager(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	row, err := a.ODB.OrchestrationByID(ctx, id)
	if err != nil {
		log.Error("cannot read the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot read orchestration %d", id)
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "orchestration %d not found", id)
	}
	if err := a.inTx(ctx, log, func(tx *cdb.DB) error {
		_, err := tx.DeleteOrchestration(ctx, id)
		return err
	}); err != nil {
		log.Error("cannot delete the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot delete orchestration %d", id)
	}
	author, _ := c.Get(XUserEmail).(string)
	if err := a.ODB.Log(ctx, cdb.LogEntry{
		Action: "orchestration.delete", User: author, Fmt: "deleted orchestration %(name)s",
		Dict: map[string]any{"name": row.Name}, Level: "info",
	}); err != nil {
		log.Error("cannot write audit log", logkey.Error, err)
	}
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Debug("cannot notify changes", logkey.Error, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"info": "orchestration deleted"})
}

// PostOrchestrationCheck handles POST /orchestrations/check: the targets of each
// step resolved now, and each checked by a dry run of its queueing.
func (a *Api) PostOrchestrationCheck(c echo.Context) error {
	log := echolog.GetLogHandler(c, "PostOrchestrationCheck")
	if err := requireManager(c); err != nil {
		return err
	}
	var in server.OrchestrationInput
	if err := c.Bind(&in); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	// An invalid step is told, its targets left out; the others are checked all
	// the same, as the editor shows them while the plan is being written.
	stepErrs := make([]string, len(in.Steps))
	valid := make([]server.OrchestrationStep, len(in.Steps))
	for i, step := range in.Steps {
		if err := validateOrchestrationStep(in.Steps, i, step); err != nil {
			stepErrs[i] = err.Error()
			continue
		}
		valid[i] = step
	}
	act := actorOf(c)
	act.dryRun = true
	data := make([]server.OrchestrationCheckStep, len(in.Steps))
	resolvedSteps := make([][]orchestrationTarget, len(in.Steps))
	for i, step := range in.Steps {
		data[i] = server.OrchestrationCheckStep{Step: i, Targets: []server.OrchestrationCheckTarget{}, Message: stepErrs[i]}
		if stepErrs[i] != "" {
			continue
		}
		var targets []orchestrationTarget
		if step.Targets.Mode == server.SameAs {
			if from := *step.Targets.Step; data[from].Message != "" {
				data[i].Message = fmt.Sprintf("step %d is not valid", from+1)
				continue
			} else {
				targets = resolvedSteps[from]
			}
		} else {
			resolved, msg, err := a.resolveStepTargets(ctx, valid[i])
			if err != nil {
				log.Error("cannot resolve the targets", "step", i, logkey.Error, err)
				return JSONProblemf(c, http.StatusInternalServerError, "cannot resolve the targets of step %d", i+1)
			}
			if msg != "" {
				data[i].Message = msg
				continue
			}
			if len(resolved) > maxOrchestrationTargets {
				data[i].Message = fmt.Sprintf("%d targets, more than %d", len(resolved), maxOrchestrationTargets)
				continue
			}
			targets = resolved
		}
		resolvedSteps[i] = targets
		if len(targets) == 0 {
			data[i].Message = "no target"
		}
		for _, t := range targets {
			check := server.OrchestrationCheckTarget{Label: t.label, Ok: true}
			if t.nodeID != "" {
				check.NodeId = &t.nodeID
			}
			if t.svcID != "" {
				check.SvcId = &t.svcID
			}
			if t.clusterID != "" {
				check.ClusterId = &t.clusterID
			}
			if _, err := a.queueOrchestrationTarget(act, log, ctx, step, t); err != nil {
				check.Ok, check.Message = false, refusalMessage(err)
			}
			data[i].Targets = append(data[i].Targets, check)
		}
	}
	return c.JSON(http.StatusOK, server.OrchestrationCheckResponse{Data: data})
}

// refusalMessage is the reason of a refused queueing, without its status.
func refusalMessage(err error) string {
	var refusal *actionRefusal
	if errors.As(err, &refusal) {
		return refusal.msg
	}
	return err.Error()
}

// PostOrchestrationRun handles POST /orchestrations/{orchestration_id}/runs:
// the plan frozen, the targets resolved now, the run started.
func (a *Api) PostOrchestrationRun(c echo.Context, id server.InPathOrchestrationId) error {
	log := echolog.GetLogHandler(c, "PostOrchestrationRun")
	if err := requireManager(c); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	row, err := a.ODB.OrchestrationByID(ctx, id)
	if err != nil {
		log.Error("cannot read the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot read orchestration %d", id)
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "orchestration %d not found", id)
	}
	o, err := toOrchestration(*row)
	if err != nil {
		log.Error("cannot read the steps of the orchestration", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot read orchestration %d", id)
	}
	if err := validateOrchestration(server.OrchestrationInput{Name: o.Name, Steps: o.Steps}); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	resolved, messages, err := a.resolveOrchestrationTargets(ctx, o.Steps)
	if err != nil {
		log.Error("cannot resolve the targets", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot resolve the targets")
	}
	for i, msg := range messages {
		if msg != "" {
			return JSONProblemf(c, http.StatusBadRequest, "step %d: %s", i+1, msg)
		}
	}
	var targets []cdb.OrchestrationRunTarget
	for step, list := range resolved {
		for position, t := range list {
			targets = append(targets, cdb.OrchestrationRunTarget{
				Step: step, Position: position, NodeID: t.nodeID, SvcID: t.svcID, ClusterID: t.clusterID, Label: t.label,
			})
		}
	}
	act := actorOf(c)
	var runID int
	if err := a.inTx(ctx, log, func(tx *cdb.DB) error {
		runID, err = tx.InsertOrchestrationRun(ctx, cdb.OrchestrationRun{
			OrchestrationID: &id, Name: o.Name, PlanJSON: row.StepsJSON, UserID: act.userID, StartedBy: act.email,
		}, len(o.Steps), targets)
		return err
	}); err != nil {
		log.Error("cannot start the run", "orchestration_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot start the run")
	}
	if err := a.ODB.Log(ctx, cdb.LogEntry{
		Action: "orchestration.run", User: act.email, Fmt: "started run %(run_id)s of orchestration %(name)s",
		Dict: map[string]any{"name": o.Name, "run_id": runID}, Level: "info",
	}); err != nil {
		log.Error("cannot write audit log", logkey.Error, err)
	}
	log.Info("orchestration run started", "orchestration_id", id, "run_id", runID, "targets", len(targets))
	// The first step starts at once rather than at the next tick.
	a.wakeOrchestrationEngine()
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Debug("cannot notify changes", logkey.Error, err)
	}
	return a.writeOrchestrationRun(c, log, runID)
}
