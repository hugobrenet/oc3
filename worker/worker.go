package worker

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/opensvc/oc3/cachekeys"
	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/util/logkey"
)

type (
	Worker struct {
		Redis *redis.Client
		DB    *sql.DB

		Queues []string
		Ev     EventPublisher

		// Runners is the maximum number of jobs to run in parallel.
		Runners int

		SubSystem string
		UploadDir string
	}

	EventPublisher interface {
		EventPublish(eventName string, data map[string]any) error
	}

	ODBSetter interface {
		ODBSetter(od *cdb.DB) error
	}

	RedisSetter interface {
		SetRedis(r *redis.Client)
	}

	EvSetter interface {
		SetEv(ev EventPublisher)
	}

	UploadDirSetter interface {
		SetUploadDir(s string)
	}

	JobRunner interface {
		Operationer

		Name() string
		Detail() string
		Logger() *slog.Logger

		runOps(ctx context.Context, jlog *slog.Logger, ops ...operation) error
	}
)

const (
	// job types
	jtDaemonPing           = "daemonPing"
	jtDaemonStatus         = "daemonStatus"
	jtInstanceAction       = "instanceAction"
	jtInstanceResourceInfo = "instanceResourceInfo"
	jtInstanceStatus       = "instanceStatus"
	jtNodeChecks           = "nodeChecks"
	jtNodeDisk             = "nodeDisk"
	jtNodeSystem           = "nodeSystem"
	jtObjectConfig         = "objectConfig"
	jtSANSwitch            = "sanSwitch"
	jtArray                = "array"

	// job or job step statuses
	jobStatusOk     = "ok"
	jobStatusFailed = "failed"
)

func (w *Worker) Run() error {
	slog.Info(fmt.Sprintf("starting %d runners for queues: %s", w.Runners, strings.Join(w.Queues, ", ")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registerQueueMetrics(w.Redis, w.Queues)
	feedJobRunners.Set(float64(w.Runners))
	jobC := make(chan []string, w.Runners)
	runSlots := make(chan bool, w.Runners)
	go func(c <-chan []string) {
		for {
			select {
			case j := <-c:
				runSlots <- true
				feedJobRunning.Inc()
				go func(j []string) {
					defer func() { <-runSlots }()
					defer feedJobRunning.Dec()
					if err := w.runJob(j); err != nil {
						slog.Error("job failed", logkey.Error, err)
					}
				}(j)
			case <-ctx.Done():
				return
			}
		}
	}(jobC)

	for {
		unqueuedCmd := w.Redis.BRPop(context.Background(), 5*time.Second, w.Queues...)
		unqueuedResult, err := unqueuedCmd.Result()
		switch err {
		case nil:
		case redis.Nil:
			continue
		default:
			slog.Error(err.Error())
			time.Sleep(time.Second)
			continue
		}
		jobC <- unqueuedResult
	}
}

func (w *Worker) runJob(unqueuedJob []string) error {
	begin := time.Now()
	var j JobRunner
	slog.Debug(fmt.Sprintf("BRPOP %s -> %s", unqueuedJob[0], unqueuedJob[1]))
	ctx := context.Background()
	switch unqueuedJob[0] {
	case cachekeys.FeedDaemonPingQ:
		j = newDaemonPing(unqueuedJob[1])
	case cachekeys.FeedDaemonStatusQ:
		j = newDaemonStatus(unqueuedJob[1])
	case cachekeys.FeedInstanceResourceInfoQ:
		objectName, nodeID, ClusterID, err := w.jobToInstanceAndClusterID(unqueuedJob[1])
		if err != nil {
			err := fmt.Errorf("invalid feed instance resource info index: %w", err)
			slog.Warn(err.Error())
			return err
		}
		j = newjobFeedInstanceResourceInfo(objectName, nodeID, ClusterID)
	case cachekeys.FeedNodeDiskQ:
		// expected unqueuedJob[1]: <nodename>@<nodeID>@<clusterID>
		l := strings.Split(unqueuedJob[1], "@")
		if len(l) != 3 || l[0] == "" || l[1] == "" || l[2] == "" {
			slog.Warn(fmt.Sprintf("invalid feed node disk index expected `nodename`@`nodeID`@`clusterID` found: %s", unqueuedJob[1]))
			return fmt.Errorf("invalid feed node disk index expected `nodename`@`nodeID`@`clusterID` found: %s", unqueuedJob[1])
		}
		j = newNodeDisk(l[0], l[1], l[2])
	case cachekeys.FeedNodeChecksQ:
		// expected unqueuedJob[1]: <nodename>@<nodeID>@<clusterID>
		l := strings.Split(unqueuedJob[1], "@")
		if len(l) != 3 || l[0] == "" || l[1] == "" || l[2] == "" {
			err := fmt.Errorf("invalid feed node checks index expected `nodename`@`nodeID`@`clusterID` found: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newNodeChecks(l[0], l[1], l[2])
	case cachekeys.FeedSANSwitchQ:
		// expected unqueuedJob[1]: <type>@<name>
		if l := strings.SplitN(unqueuedJob[1], "@", 2); len(l) != 2 || l[0] == "" || l[1] == "" {
			err := fmt.Errorf("invalid feed san switch index expected `type`@`name` found: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newSANSwitch(unqueuedJob[1])
	case cachekeys.FeedArrayQ:
		// expected unqueuedJob[1]: <type>@<name>
		if l := strings.SplitN(unqueuedJob[1], "@", 2); len(l) != 2 || l[0] == "" || l[1] == "" {
			err := fmt.Errorf("invalid feed array index expected `type`@`name` found: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newArray(unqueuedJob[1])
	case cachekeys.FeedObjectConfigQ:
		objectName, nodeID, ClusterID, err := w.jobToInstanceAndClusterID(unqueuedJob[1])
		if err != nil {
			err := fmt.Errorf("invalid feed instance config index: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newFeedObjectConfig(objectName, nodeID, ClusterID)
	case cachekeys.FeedSystemQ:
		j = newDaemonSystem(unqueuedJob[1])
	case cachekeys.FeedInstanceStatusQ:
		objectName, nodeID, clusterID, err := w.jobToInstanceAndClusterID(unqueuedJob[1])
		if err != nil {
			err := fmt.Errorf("invalid feed instance status index: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newInstanceStatus(objectName, nodeID, clusterID)

	case cachekeys.FeedInstanceActionQ:
		objectName, nodeID, ClusterID, uuid, err := w.jobToInstanceClusterIdAndUuid(unqueuedJob[1])
		if err != nil {
			err := fmt.Errorf("invalid feed begin action index: %s", unqueuedJob[1])
			slog.Warn(err.Error())
			return err
		}
		j = newAction(objectName, nodeID, ClusterID, uuid)

	default:
		slog.Debug(fmt.Sprintf("ignore queue '%s'", unqueuedJob[0]))
		return nil
	}
	jName := j.Name()
	jlog := j.Logger()

	if a, ok := j.(RedisSetter); ok {
		a.SetRedis(w.Redis)
	}

	if a, ok := j.(ODBSetter); ok {
		// Each job its own database helper and session, never shared with the
		// jobs the other runners run meanwhile: a job may install its transaction
		// in the helper (CreateTx), and announces its own changes, scoped to the
		// objects it fed.
		odb := cdb.New(w.DB)
		odb.CreateSession(w.Ev)
		if err := a.ODBSetter(odb); err != nil {
			jlog.Error("🔴ODBSetter failed", logkey.Error, err)
			return fmt.Errorf("can't setup cdb for %s: %w", jName, err)
		}
	}

	if a, ok := j.(RedisSetter); ok {
		a.SetRedis(w.Redis)
	}

	if a, ok := j.(EvSetter); ok {
		a.SetEv(w.Ev)
	}

	if a, ok := j.(UploadDirSetter); ok {
		a.SetUploadDir(w.UploadDir)
	}

	status := jobStatusOk
	err := RunJob(ctx, j)
	duration := time.Since(begin)
	if err != nil {
		status = jobStatusFailed
		jlog.Error("🔴job failure", logkey.Error, err, logkey.JobDetail, j.Detail())
	}
	feedJobCounter.With(prometheus.Labels{"job_type": jName, "status": status}).Inc()
	feedJobDuration.With(prometheus.Labels{"job_type": jName, "status": status}).Observe(duration.Seconds())
	jlog.Debug(fmt.Sprintf("BRPOP %s <- %s: %s", unqueuedJob[0], unqueuedJob[1], duration))
	return nil
}

// jobToInstanceAndClusterID splits a jobName string into path, nodeID, and clusterID based on "@" delimiter.
// Returns an error if the format is invalid or elements are empty.
// Expected jobName: foo@<nodeID>@<clusterID>
func (w *Worker) jobToInstanceAndClusterID(jobName string) (path, nodeID, clusterID string, err error) {
	l := strings.Split(jobName, "@")
	if len(l) != 3 || l[0] == "" || l[1] == "" || l[2] == "" {
		err = fmt.Errorf("unexpected job name: %s", jobName)
	} else {
		path = l[0]
		nodeID = l[1]
		clusterID = l[2]
	}
	return
}

func (w *Worker) jobToInstanceClusterIdAndUuid(jobName string) (path, nodeID, clusterID, uuid string, err error) {
	l := strings.Split(jobName, ":")
	if len(l) != 2 {
		err = fmt.Errorf("unexpected job name: %s", jobName)
		return
	}
	if path, nodeID, clusterID, err = w.jobToInstanceAndClusterID(l[0]); err != nil {
		err = fmt.Errorf("unexpected job name: %s", jobName)
		return
	}
	uuid = l[1]
	return
}
