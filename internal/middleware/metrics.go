package middleware

import (
	"CalfGateway/internal/monitor"
	"net/http"

	"github.com/gin-gonic/gin"
)

func MetricsMiddleware(mon *monitor.Monitor, routeName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		errType := monitor.ErrNone
		if degraded, ok := c.Get("degraded"); ok && degraded == true {
			errType = monitor.ErrDegraded
		} else if c.Writer.Status() >= http.StatusInternalServerError {
			errType = monitor.ErrBackend5xx
		}
		mon.Record(routeName, errType)
	}
}
