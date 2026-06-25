package middleware

import (
	"CalfGateway/internal/degradation"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

func DegradationMiddleware(strategy degradation.Strategy, judge *degradation.Judge) gin.HandlerFunc {
	return func(c *gin.Context) {
		degrade, reason := judge.ShouldDegrade()
		if !degrade {
			c.Next()
			return
		}

		c.Set("degraded", true)
		resp, err := strategy.Execute(c.Request.Context(), c.Request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service degraded",
			})
			return
		}

		c.Header("X-Degradation-Reason", reason.String())
		writeResponse(c, resp)
		c.Abort()
	}
}

func writeResponse(c *gin.Context, resp *http.Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.Writer.Write(body)
}
