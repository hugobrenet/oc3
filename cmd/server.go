package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth"
	"github.com/shaj13/go-guardian/v2/auth/strategies/union"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/safe"
	api "github.com/opensvc/oc3/server"
	handlers "github.com/opensvc/oc3/server/handlers"
	"github.com/opensvc/oc3/xauth"
)

type (
	server struct {
		db      *sql.DB
		section string
		redis   *redis.Client
		// oidc is the OpenID Connect sign-in; nil when server.oidc.enable is false.
		oidc *xauth.OIDC
		safe *safe.Service
		// assistant is the AI agent the assistant routes are relayed to.
		assistant *handlers.AssistantConfig
	}
)

func newServer() (*server, error) {
	// A statement of the API stops after server.db.max_statement_time: the
	// database goes on running a query whose request was given up, the client
	// gone, until it ends.
	var vars map[string]string
	if d := viper.GetDuration(sectionServer + ".db.max_statement_time"); d > 0 {
		vars = map[string]string{"max_statement_time": strconv.FormatFloat(d.Seconds(), 'f', -1, 64)}
	}
	db, err := newDatabaseWith(sectionServer, vars)
	if err != nil {
		return nil, err
	}
	t := &server{db: db, section: sectionServer, redis: newRedis()}
	// A keys file configured but unreadable stops the server: it would otherwise
	// store in plaintext what the operator asked to encrypt.
	if t.safe, err = newSafeService(); err != nil {
		return nil, err
	}
	if t.assistant, err = newAssistant(t.section); err != nil {
		return nil, err
	}
	// A wrong OIDC configuration stops the server rather than leaving it to run
	// with weaker settings than the operator asked for.
	if t.oidc, err = newOIDC(context.Background(), t.section, t.redis, db); err != nil {
		return nil, err
	}
	// Turning the passwords off leaves the identity provider as the only way in:
	// without it, no user could sign in.
	if !viper.GetBool(t.section+".auth.basic_users") && t.oidc == nil {
		return nil, fmt.Errorf("%[1]s.auth.basic_users is false and %[1]s.oidc.enable is not set: no user could sign in", t.section)
	}
	return t, nil
}

func (t *server) Section() string { return t.section }

func (t *server) apiRegister(e *echo.Echo) {
	odb := cdb.New(t.db)
	handler := &handlers.Api{
		DB:          t.db,
		ODB:         odb,
		Redis:       t.redis,
		UI:          viper.GetBool(t.section + ".ui.enable"),
		SyncTimeout: viper.GetDuration(t.section + ".sync.timeout"),
		SubSystem:   t.section,
		OIDC:        t.oidc,
		BasicUsers:  viper.GetBool(t.section + ".auth.basic_users"),
		Safe:        t.safe,
		Assistant:   t.assistant,
	}
	// With a messenger, the changes made through the api are announced like those
	// of the workers, and the websocket clients get their tokens from the api.
	if viper.GetString("messenger.url") != "" {
		ev := newEv()
		odb.CreateSession(ev)
		handler.Ev = ev
		handler.Realtime = ev
	} else {
		odb.CreateSession(nil)
	}
	// The /agent routes are the nodes' own: no user may call them.
	e.Use(handlers.AgentMiddleware(pathApi + "/agent"))
	api.RegisterHandlersWithBaseURL(e, handler, pathApi)
	// The orchestration runs are advanced by the api server, for as long as it
	// runs: a named lock lets one server do it when there are several.
	go handler.RunOrchestrationEngine(context.Background())
}

func (t *server) docMiddleware() echo.MiddlewareFunc {
	return handlers.UIMiddleware(context.Background(), pathApi, pathSpec)
}

// authMiddleware authenticates the request, then lets a member of the
// Manager make it as another user, and marks the request with the client it
// declares coming through, for the audit.
func (t *server) authMiddleware(publicPath, publicPrefix []string) echo.MiddlewareFunc {
	authenticate := t.authenticateMiddleware(publicPath, publicPrefix)
	// Right after authentication: a request riding on the session cookie must come
	// from the SPA before anything acts on it, impersonation included.
	csrf := handlers.CSRFMiddleware(t.oidc)
	impersonate := handlers.ImpersonateMiddleware(t.db)
	origin := handlers.OriginMiddleware()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return authenticate(csrf(impersonate(origin(next))))
	}
}

func (t *server) authenticateMiddleware(publicPath, publicPrefix []string) echo.MiddlewareFunc {
	// The endpoints of the OpenID Connect sign-in lead to the authentication: they
	// are public, each checking what it needs. Listed one by one, so that the
	// other /auth endpoints keep their own strategies; login and callback by
	// prefix, as their query string is part of the request URI.
	publicPath = append(publicPath,
		pathApi+"/auth/info", pathApi+"/auth/logout", pathApi+"/auth/backchannel-logout")
	publicPrefix = append(publicPrefix, pathApi+"/auth/login", pathApi+"/auth/callback")
	// The archive of the compliance modules for the v2 agents, which fetch it
	// without credentials, when the configuration allows it; the handler checks
	// the same setting. With and without a query string: the strategy compares
	// the whole request URI.
	if viper.GetBool("server.compliance_modules.anonymous_download") {
		archive := pathApi + "/compliance/modules_repo/current"
		publicPath = append(publicPath, archive)
		publicPrefix = append(publicPrefix, archive+"?")
	}

	strategies := []auth.Strategy{
		xauth.NewPublicStrategy(publicPath, publicPrefix),
		xauth.NewAnonRegister(),
	}
	if t.oidc != nil {
		strategies = append(strategies, xauth.NewOIDCSession(t.oidc, t.db), xauth.NewOIDCBearer(t.oidc, t.db))
	}
	if viper.GetBool(t.section + ".auth.basic_users") {
		strategies = append(strategies, xauth.NewBasicWeb2py(t.db, viper.GetString("w2p_hmac")))
	}
	strategies = append(strategies, xauth.NewBasicNode(t.db))
	return handlers.AuthMiddleware(union.New(strategies...))
}
