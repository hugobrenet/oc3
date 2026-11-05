package cmd

import (
	"context"
	"database/sql"

	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth/strategies/union"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	api "github.com/opensvc/oc3/feeder"
	handlers "github.com/opensvc/oc3/feeder/handlers"
	"github.com/opensvc/oc3/xauth"
)

type (
	feeder struct {
		db      *sql.DB
		section string
		redis   *redis.Client
	}
)

func newFeeder() (*feeder, error) {
	if db, err := newDatabase(sectionFeeder); err != nil {
		return nil, err
	} else {
		return &feeder{db: db, section: sectionFeeder, redis: newRedis()}, nil
	}
}

func (t *feeder) Section() string { return t.section }

func (t *feeder) apiRegister(e *echo.Echo) {
	odb := cdb.New(t.db)
	// With a messenger, what the agents change through the feeder — the progress
	// of the queued actions — is announced like the changes of the workers.
	if viper.GetString("messenger.url") != "" {
		odb.CreateSession(newEv())
	} else {
		odb.CreateSession(nil)
	}

	api.RegisterHandlersWithBaseURL(e, &handlers.Api{
		DB:  t.db,
		ODB: odb,

		Redis:        t.redis,
		UI:           viper.GetBool(t.section + ".ui.enable"),
		SyncTimeout:  viper.GetDuration(t.section + ".sync.timeout"),
		ArrayMaxSize: viper.GetInt64(t.section + ".array.max_size"),
	}, pathApi)
}

func (t *feeder) docMiddleware() echo.MiddlewareFunc {
	return handlers.UIMiddleware(context.Background(), pathApi, pathSpec)
}

func (t *feeder) authMiddleware(publicPath, publicPrefix []string) echo.MiddlewareFunc {
	return handlers.AuthMiddleware(union.New(
		xauth.NewPublicStrategy(publicPath, publicPrefix),
		xauth.NewBasicNode(t.db),
	))
}
