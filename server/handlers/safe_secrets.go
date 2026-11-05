package serverhandlers

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
)

// GetSafeSecrets handles GET /safe/secrets: the rows holding secrets sealed
// with the safe master key, never their values. Each kind to those managing
// it: the connectors to the FormsManagers, the channels to the AlertsManagers,
// all to the Managers.
func (a *Api) GetSafeSecrets(c echo.Context) error {
	if !IsAuthByUser(c) {
		return httpProblem(c, httpErrorf(http.StatusUnauthorized, "user authentication required"))
	}
	var kinds []string
	if HasGroup(c, "FormsManager") {
		kinds = append(kinds, string(server.SafeSecretFormConnector))
	}
	if HasGroup(c, "AlertsManager") {
		kinds = append(kinds, string(server.SafeSecretNotificationChannel))
	}
	if !IsManager(c) && len(kinds) == 0 {
		return httpProblem(c, httpErrorf(http.StatusForbidden, "user has no Manager, FormsManager nor AlertsManager privilege"))
	}
	if IsManager(c) {
		kinds = nil
	}
	refs, err := a.ODB.SealedSecrets(c.Request().Context(), kinds...)
	if err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "GetSafeSecrets"), "cannot read the secrets", err))
	}
	ring := a.safeRing()
	out := server.SafeSecretsResponse{Data: make([]server.SafeSecret, 0, len(refs))}
	if ring != nil && ring.Active() != nil {
		id := ring.Active().ID
		out.ActiveKeyId = &id
	}
	for _, r := range refs {
		item := server.SafeSecret{
			Kind:    server.SafeSecretKind(r.Table.Kind),
			Id:      r.ID,
			Name:    r.Name,
			KeyId:   r.KeyID,
			Updated: r.Updated.Format(time.RFC3339),
		}
		if ring != nil {
			item.KeyKnown = ring.Get(r.KeyID) != nil
			item.KeyActive = ring.Active() != nil && ring.Active().ID == r.KeyID
		}
		out.Data = append(out.Data, item)
	}
	return c.JSON(http.StatusOK, out)
}
