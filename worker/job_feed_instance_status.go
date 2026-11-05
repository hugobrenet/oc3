package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/opensvc/oc3/cachekeys"
	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/util/logkey"
)

type jobFeedInstanceStatus struct {
	JobBase
	JobRedis
	JobDB

	// idX is the id of the posted instance config with the expected pattern: <objectName>@<nodeID>@<clusterID>:<requestId>
	idX string

	objectName string

	// objectID is db ID of the object found or created in the database
	objectID string

	// nodeID is db ID of the node that have posted object data
	nodeID string

	// clusterID is the db cluster ID of the node that have posted object data
	clusterID string

	// data is the posted instance status
	status *instanceData

	rawData []byte

	data instancer

	node *cdb.DBNode

	obj *cdb.DBObject
}

func newInstanceStatus(objectName, nodeID, clusterID string) *jobFeedInstanceStatus {
	idX := fmt.Sprintf("%s@%s@%s", objectName, nodeID, clusterID)
	return &jobFeedInstanceStatus{
		JobBase: JobBase{
			name:   jtInstanceStatus,
			detail: "ID: " + idX,
			logger: slog.With(logkey.NodeID, nodeID, logkey.ClusterID, clusterID, logkey.Object, objectName, logkey.JobName, jtInstanceStatus),
		},
		JobRedis: JobRedis{
			cachePendingH:   cachekeys.FeedInstanceStatusPendingH,
			cachePendingIDX: idX,
		},
		idX:        idX,
		nodeID:     nodeID,
		clusterID:  clusterID,
		objectName: objectName,
	}
}

func (d *jobFeedInstanceStatus) Operations() []operation {
	return []operation{
		{name: "dropPending", do: d.dropPending},
		{name: "findNodeFromDb", do: d.findNodeFromDb},
		{name: "getData", do: d.getData},
		{name: "dbNow", do: d.dbNow},
		{name: "findObjectFromDb", do: d.findObjectFromDb},
		{name: "updateDB", do: d.updateDB},
		{name: "pushFromTableChanges", do: d.pushFromTableChanges},
		{name: "processed", do: d.processed},
	}
}

func (d *jobFeedInstanceStatus) getData(ctx context.Context) error {
	var (
		data map[string]any
	)
	if b, err := d.redis.HGet(ctx, cachekeys.FeedInstanceStatusH, d.idX).Bytes(); err != nil {
		return fmt.Errorf("getData: HGET %s %s: %w", cachekeys.FeedInstanceStatusH, d.idX, err)
	} else if err = json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("getData: unexpected data from %s %s: %w", cachekeys.FeedInstanceStatusH, d.idX, err)
	} else {
		d.rawData = b
		var nilMap map[string]any
		clientVersion := mapToS(data, "", "version")
		switch {
		case strings.HasPrefix(clientVersion, "2."):
			d.data = &instanceStatusV2{data: mapToMap(data, nilMap, "data")}
		default:
			return fmt.Errorf("no mapper for version %s", clientVersion)
		}
	}

	d.status = d.data.InstanceStatus(
		d.objectName,
		d.node.Nodename)
	if d.status == nil {
		return fmt.Errorf("no instance status for object %s node %s", d.objectName, d.nodeID)
	}
	slog.Debug(fmt.Sprintf("got instance data for objectname %s nodeid %s:%#v", d.objectName, d.nodeID, d.status))

	return nil
}

func (d *jobFeedInstanceStatus) findNodeFromDb(ctx context.Context) error {
	if n, err := d.oDb.NodeByNodeID(ctx, d.nodeID); err != nil {
		return fmt.Errorf("findFromDb: node %s: %w", d.nodeID, err)
	} else {
		d.node = n
	}
	slog.Debug(fmt.Sprintf("jobFeedInstanceStatus found node %s for id %s", d.node.Nodename, d.nodeID))

	return nil

}

func (d *jobFeedInstanceStatus) findObjectFromDb(ctx context.Context) error {
	if isNew, objId, err := d.oDb.ObjectIDFindOrCreate(ctx, d.objectName, d.clusterID); err != nil {
		return fmt.Errorf("find or create object ID failed for %s: %w", d.objectName, err)
	} else if isNew {
		// TODO: add metrics
		slog.Debug(fmt.Sprintf("jobFeedInstanceStatus has created new object id %s@%s %s", d.objectName, d.clusterID, objId))
	} else {
		d.objectID = objId
	}

	if obj, err := d.oDb.ObjectFromID(ctx, d.objectID); err != nil {
		return fmt.Errorf("findFromDb: object %s: %w", d.objectID, err)
	} else if obj == nil {
		return fmt.Errorf("findFromDb: object %s: not found", d.objectID)
	} else if obj.Svcname == "" {
		// repaired by the next daemon status of its cluster, see ObjectCreate
		return fmt.Errorf("findFromDb: object %s: service row without svcname", d.objectID)
	} else {
		d.obj = obj
	}
	slog.Debug(fmt.Sprintf("jobFeedInstanceStatus found object %s for id %s", d.objectName, d.objectID))

	return nil
}

func (d *jobFeedInstanceStatus) updateDB(ctx context.Context) error {
	details, err := d.extractInstanceDetails(
		ctx,
		d.status,
		d.obj,
		d.node)
	if err != nil {
		return err
	}
	if len(details.svcmonL) > 0 {
		if err := d.oDb.SvcmonUpdate(ctx, details.svcmonL...); err != nil {
			return err
		}
	}
	// TODO: details...

	return nil
}

func (d *jobFeedInstanceStatus) processed(ctx context.Context) error {
	_ = d.redis.Publish(ctx, cachekeys.FeedInstanceStatusP, d.idX)
	return nil
}

// pushFromTableChanges announces the changes as concerning the instance the node posted: its object and the node.
func (d *jobFeedInstanceStatus) pushFromTableChanges(ctx context.Context) error {
	return d.pushScopedTableChanges(ctx, []string{d.objectID}, []string{d.nodeID})
}
