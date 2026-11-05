package serverhandlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/notify"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	utilmail "github.com/opensvc/oc3/util/mail"
)

// notificationsMailEnabled tells whether the scheduler mails the alerts: the
// notifications enabled, and an SMTP server.
func notificationsMailEnabled() bool {
	if !viper.GetBool("notifications.enable") {
		return false
	}
	return utilmail.ConfigFromViper("notifications.mail").IsSet() || utilmail.ConfigFromViper("server.mail").IsSet()
}

// userNotificationsResponse answers the settings of the user userID.
func (a *Api) userNotificationsResponse(c echo.Context, userID int64) error {
	log := echolog.GetLogHandler(c, "GetUserSelfNotifications")
	s, found, err := a.ODB.UserNotifySettingsOf(c.Request().Context(), userID)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the settings", err))
	}
	if !found {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "user %d not found", userID))
	}
	out := server.UserNotifications{
		Email:                   s.Email,
		EmailNotifications:      s.Enabled,
		EmailLogLevel:           server.UserNotificationLevel(s.LogLevel),
		EmailNotificationsDelay: s.DelayMin,
		Periods:                 map[string]server.NotificationPeriod{},
		MailEnabled:             notificationsMailEnabled(),
	}
	if s.Prefs != "" {
		var doc struct {
			Periods map[string]server.NotificationPeriod `json:"notifications_periods"`
		}
		if err := json.Unmarshal([]byte(s.Prefs), &doc); err == nil && doc.Periods != nil {
			out.Periods = doc.Periods
		}
	}
	return c.JSON(http.StatusOK, out)
}

// GetUserSelfNotifications handles GET /users/self/notifications.
func (a *Api) GetUserSelfNotifications(c echo.Context) error {
	userID := authUserID(c)
	if !IsAuthByUser(c) || userID == nil {
		return httpProblem(c, httpErrorf(http.StatusUnauthorized, "user authentication required"))
	}
	return a.userNotificationsResponse(c, *userID)
}

// PutUserSelfNotifications handles PUT /users/self/notifications.
func (a *Api) PutUserSelfNotifications(c echo.Context) error {
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "PutUserSelfNotifications")
	userID := authUserID(c)
	if !IsAuthByUser(c) || userID == nil {
		return httpProblem(c, httpErrorf(http.StatusUnauthorized, "user authentication required"))
	}
	if IsImpersonating(c) {
		return httpProblem(c, httpErrorf(http.StatusForbidden, "the notification settings cannot be changed while impersonating"))
	}
	var in server.UserNotificationsInput
	if err := c.Bind(&in); err != nil {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "invalid body: %s", err))
	}
	if !in.EmailLogLevel.Valid() {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "email_log_level: debug, info, warning, error or critical"))
	}
	if in.EmailNotificationsDelay < 0 || in.EmailNotificationsDelay > notifyMaxDelayMin {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "email_notifications_delay: 0 to %d minutes", notifyMaxDelayMin))
	}
	current, found, err := a.ODB.UserNotifySettingsOf(ctx, *userID)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the settings", err))
	}
	if !found {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "user %d not found", *userID))
	}
	// The periods go in the preferences document, the other keys kept.
	prefs := map[string]any{}
	if current.Prefs != "" {
		if err := json.Unmarshal([]byte(current.Prefs), &prefs); err != nil {
			prefs = map[string]any{}
		}
	}
	if in.Periods == nil || len(*in.Periods) == 0 {
		delete(prefs, "notifications_periods")
	} else {
		b, _ := json.Marshal(*in.Periods)
		if err := notify.ValidatePeriods(string(b)); err != nil {
			return httpProblem(c, httpErrorf(http.StatusBadRequest, "periods: %s", err))
		}
		prefs["notifications_periods"] = *in.Periods
	}
	doc, err := json.Marshal(prefs)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot encode the preferences", err))
	}
	current.Enabled = in.EmailNotifications
	current.LogLevel = string(in.EmailLogLevel)
	current.DelayMin = in.EmailNotificationsDelay
	current.Prefs = string(doc)
	if err := a.ODB.SetUserNotifySettings(ctx, *userID, current); err != nil {
		return httpProblem(c, httpInternal(log, "cannot save the settings", err))
	}
	a.notifyAudit(c, "user.change", "change user %(email)s: notifications by mail %(enabled)s, from %(level)s, delay %(delay)s min",
		map[string]any{"email": current.Email, "enabled": strconv.FormatBool(current.Enabled), "level": current.LogLevel, "delay": strconv.Itoa(current.DelayMin)})
	return a.userNotificationsResponse(c, *userID)
}

// GetNotificationDeliveries handles GET /notifications/deliveries.
func (a *Api) GetNotificationDeliveries(c echo.Context, params server.GetNotificationDeliveriesParams) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.handleList(c, "GetNotificationDeliveries", "notification_delivery", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetNotifyDeliveries(ctx, p)
	})
}
