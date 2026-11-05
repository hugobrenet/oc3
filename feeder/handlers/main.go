package feederhandlers

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/feeder"
	"github.com/opensvc/oc3/util/echolog"
)

type (
	Api struct {
		DB    *sql.DB
		ODB   *cdb.DB
		Redis *redis.Client
		UI    bool

		// SyncTimeout is the timeout for synchronous api calls
		SyncTimeout time.Duration

		// ArrayMaxSize is the largest array configuration accepted, in
		// bytes, once decompressed.
		ArrayMaxSize int64
	}
)

var (
	SCHEMA openapi3.T
)

func init() {
	if schema, err := feeder.GetSwagger(); err == nil {
		SCHEMA = *schema
	}
}

func getNodeIDAndLogger(c echo.Context, handler string) (string, *slog.Logger) {
	log := echolog.GetLogHandler(c, handler)
	nodeID := nodeIDFromContext(c)
	if nodeID == "" {
		log.Debug("empty node id")
	}
	return nodeID, log
}

func getNodeIDClusterIDAndLogger(c echo.Context, handler string) (string, string, *slog.Logger) {
	nodeID, log := getNodeIDAndLogger(c, handler)
	clusterID := clusterIDFromContext(c)
	return nodeID, clusterID, log
}
