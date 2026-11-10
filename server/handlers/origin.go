package serverhandlers

import (
	"regexp"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
)

const (
	// OriginHeader names the client a request comes through: assistant for the
	// AI assistant, through the MCP of the collector and its agent, mcp for
	// another client of that MCP. The MCP sets it on its calls.
	OriginHeader = "X-OpenSVC-Origin"
	// RequestIDHeader is the ID the client gave the request, which the agent and
	// the MCP write on their audit lines.
	RequestIDHeader = "X-Request-ID"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// OriginMiddleware marks the context of a request that declares the client it
// comes through (OriginHeader), with its request ID (RequestIDHeader), so that
// the audit entries written while handling it say so (see cdb.Log). An origin
// that is none of the known ones is ignored, a malformed request ID too.
//
// The client declares it: nothing proves it. The audit entry tells how an
// action was made, never who made it, which the authentication decides.
func OriginMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header
			name := header.Get(OriginHeader)
			if name != cdb.OriginAssistant && name != cdb.OriginMCP {
				return next(c)
			}
			origin := cdb.Origin{Name: name}
			if id := header.Get(RequestIDHeader); requestIDPattern.MatchString(id) {
				origin.RequestID = id
			}
			c.SetRequest(c.Request().WithContext(cdb.WithOrigin(c.Request().Context(), origin)))
			return next(c)
		}
	}
}
