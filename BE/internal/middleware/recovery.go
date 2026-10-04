package middleware

import (
	"fmt"
	"runtime"

	"github.com/ku/ku-backend/pkg/response"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

func Recovery() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 64<<10)
					n := runtime.Stack(buf, false)
					log.WithFields(response.LogFields(c)).WithFields(log.Fields{
						"panic": fmt.Sprintf("%v", r),
						"stack": string(buf[:n]),
					}).Error("panic recovered")
					_ = response.InternalError(c)
				}
			}()
			return next(c)
		}
	}
}
