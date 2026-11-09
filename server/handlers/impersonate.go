package serverhandlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
	"github.com/opensvc/oc3/xauth"
)

const (
	// ImpersonateHeader carries the auth_user.id a request is made as.
	ImpersonateHeader = "OC3-Impersonate"
	// ImpersonatedByHeader answers a request made as another user with the email
	// of the user who really signed in.
	ImpersonatedByHeader = "OC3-Impersonated-By"
	// ImpersonationRefusedHeader marks a refusal due to the impersonation itself,
	// so that the client can stop impersonating rather than retry.
	ImpersonationRefusedHeader = "OC3-Impersonation-Refused"

	XImpersonator = "XImpersonator"
)

// ImpersonateMiddleware lets a Manager act as another user, as the impersonation
// of the historical collector does (/init/default/user/impersonate), which
// required the Impersonate group instead: a request carrying the
// ImpersonateHeader is handled with the identity, the groups and so the visibility of that user. The
// collector being stateless here, the header is checked on every request, and
// the identity of the user who really signed in stays available to the handlers
// (RealUserInfo) and in the server log.
//
// Every audit entry of a request made as another user names both users (see
// cdb.Log), and each such request that changes something is logged on its own
// (logImpersonatedRequest), whether its handler logs or not.
//
// It must run after AuthMiddleware.
func ImpersonateMiddleware(db *sql.DB) echo.MiddlewareFunc {
	audit := cdb.New(db)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ident := c.Request().Header.Get(ImpersonateHeader)
			if ident == "" {
				return next(c)
			}
			// A public route (/auth/info, the OIDC sign-in endpoints...) is
			// served to nobody in particular: the SPA sends the header with every
			// request, refusing it there would end the impersonation at once.
			if _, authenticated := c.Get(XAuthMode).(string); !authenticated {
				return next(c)
			}
			refuse := func(code int, format string, args ...any) error {
				c.Response().Header().Set(ImpersonationRefusedHeader, "true")
				return JSONProblemf(c, code, format, args...)
			}
			signedIn := UserInfoFromContext(c)
			if signedIn == nil || !IsAuthByUser(c) {
				return refuse(http.StatusForbidden, "only a signed-in user can impersonate")
			}
			if !IsManager(c) {
				return refuse(http.StatusForbidden, "impersonating requires the Manager privilege")
			}
			if _, ok := xauth.ParseUserID(ident); !ok {
				return refuse(http.StatusBadRequest, "%s must be a user id", ImpersonateHeader)
			}
			if ident == signedIn.GetExtensions().Get(xauth.XUserID) {
				return next(c)
			}
			target, err := xauth.LoadUserInfo(c.Request().Context(), db, ident)
			switch {
			case errors.Is(err, xauth.ErrUnknownUser):
				return refuse(http.StatusForbidden, "user %s does not exist", ident)
			case err != nil:
				echolog.GetLog(c).Error("cannot load the impersonated user", logkey.Error, err)
				return JSONProblem(c, http.StatusServiceUnavailable, "authentication is unavailable, the database cannot be reached; retry later")
			}
			setImpersonated(c, signedIn, target)
			err = next(c)
			logImpersonatedRequest(c, audit, err)
			return err
		}
	}
}

// setImpersonated replaces the identity of the request by target, keeping signedIn
// as the impersonator.
func setImpersonated(c echo.Context, signedIn, target auth.Info) {
	c.Set(XImpersonator, signedIn)
	c.Set("user", target)
	c.Set("groups", target.GetGroups())
	c.Set(XUserEmail, target.GetExtensions().Get(xauth.XUserEmail))
	// The audit entries written while handling the request name the impersonator.
	c.SetRequest(c.Request().WithContext(cdb.WithImpersonator(c.Request().Context(), signedIn.GetUserName())))
	c.Response().Header().Set(ImpersonatedByHeader, signedIn.GetUserName())
	echolog.GetLog(c).Debug("impersonated request", "impersonator", signedIn.GetUserName(), "user", target.GetUserName())
}

// unloggedImpersonatedRoutes change nothing an audit would follow: a one-time
// token for the live updates, asked at each page load.
var unloggedImpersonatedRoutes = map[string]bool{
	"/api/realtime/token": true,
}

// logImpersonatedRequest writes the audit entry of a request made as another user
// that changes something (any method but GET, HEAD and OPTIONS): the method, the
// path and the status answered, under the impersonated user, the impersonator
// beside. A refused or failed request is logged as well, as a warning: it was
// attempted under that identity.
func logImpersonatedRequest(c echo.Context, audit *cdb.DB, handlerErr error) {
	method := c.Request().Method
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return
	}
	if unloggedImpersonatedRoutes[c.Path()] {
		return
	}
	status := c.Response().Status
	if !c.Response().Committed {
		var he *echo.HTTPError
		switch {
		case errors.As(handlerErr, &he):
			status = he.Code
		case handlerErr != nil:
			status = http.StatusInternalServerError
		}
	}
	level := "info"
	if status >= 400 {
		level = "warning"
	}
	target := UserInfoFromContext(c)
	impersonator := RealUserInfo(c)
	// The request may be over: the entry is written whatever becomes of it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 2*time.Second)
	defer cancel()
	if err := audit.Log(ctx, cdb.LogEntry{
		Action:       "impersonation.request",
		User:         target.GetUserName(),
		Impersonator: impersonator.GetUserName(),
		Fmt:          "%(impersonator)s acting as %(user)s: %(method)s %(path)s answered %(status)d",
		Dict: map[string]any{
			"impersonator": impersonator.GetUserName(),
			"user":         target.GetUserName(),
			"method":       method,
			"path":         c.Request().URL.Path,
			"status":       status,
		},
		Level: level,
	}); err != nil {
		echolog.GetLog(c).Error("cannot write the impersonation audit entry", logkey.Error, err)
	}
}

// RealUserInfo returns the user who really signed in: the impersonator when the
// request is made as another user, the authenticated user otherwise.
func RealUserInfo(c echo.Context) auth.Info {
	if signedIn, ok := c.Get(XImpersonator).(auth.Info); ok {
		return signedIn
	}
	return UserInfoFromContext(c)
}

// IsImpersonating reports whether the request is made as another user.
func IsImpersonating(c echo.Context) bool {
	_, ok := c.Get(XImpersonator).(auth.Info)
	return ok
}

// GetImpersonation handles GET /impersonation: whether the user who signed in
// holds the Manager privilege, which impersonating requires, and who the request
// is made as.
func (a *Api) GetImpersonation(c echo.Context) error {
	if !IsAuthByUser(c) {
		return JSONProblemf(c, http.StatusUnauthorized, "user authentication required")
	}
	signedIn := RealUserInfo(c)
	resp := server.ImpersonationStatus{
		Allowed: slices.Contains(signedIn.GetGroups(), "Manager"),
	}
	if IsImpersonating(c) {
		target := UserInfoFromContext(c)
		id, _ := xauth.ParseUserID(target.GetID())
		resp.Impersonating = &server.Impersonation{UserId: id, Email: target.GetUserName(), Groups: target.GetGroups()}
	}
	return c.JSON(http.StatusOK, resp)
}

// PostUserImpersonate handles POST /users/{user_id}/impersonate: checks that the
// caller may impersonate that user and logs the start of the impersonation, as
// the historical collector logs "User %(id)s is impersonating %(other_id)s". The
// requests made as that user then carry the OC3-Impersonate header.
func (a *Api) PostUserImpersonate(c echo.Context, userId string) error {
	log := echolog.GetLogHandler(c, "PostUserImpersonate")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	if !IsAuthByUser(c) {
		return JSONProblemf(c, http.StatusUnauthorized, "user authentication required")
	}
	if IsImpersonating(c) {
		return JSONProblemf(c, http.StatusConflict, "stop impersonating before impersonating another user")
	}
	if !IsManager(c) {
		return JSONProblemf(c, http.StatusForbidden, "impersonating requires the Manager privilege")
	}
	signedIn := UserInfoFromContext(c)
	target, err := xauth.LoadUserInfo(ctx, a.ODB.DB, userId)
	switch {
	case errors.Is(err, xauth.ErrUnknownUser):
		return JSONProblemf(c, http.StatusNotFound, "user %s does not exist", userId)
	case err != nil:
		log.Error("cannot load the user to impersonate", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot load the user to impersonate")
	}
	if target.GetID() == signedIn.GetID() {
		return JSONProblemf(c, http.StatusBadRequest, "a user cannot impersonate themselves")
	}

	if logErr := a.ODB.Log(ctx, cdb.LogEntry{
		Action: "user.impersonate",
		User:   signedIn.GetUserName(),
		Fmt:    "User %(id)s is impersonating %(other_id)s",
		Dict:   map[string]any{"id": signedIn.GetUserName(), "other_id": target.GetUserName()},
		Level:  "info",
	}); logErr != nil {
		log.Error("cannot write audit log", logkey.Error, logErr)
	}

	id, _ := xauth.ParseUserID(target.GetID())
	return c.JSON(http.StatusOK, server.Impersonation{
		UserId: id,
		Email:  target.GetUserName(),
		Groups: target.GetGroups(),
	})
}
