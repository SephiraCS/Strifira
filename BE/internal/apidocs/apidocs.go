// Package apidocs serves the OpenAPI description of ku's HTTP surface.
//
// The document (openapi.yaml) is the single source of truth for the REST
// API and is embedded in the binary. It is published same-origin at
// /api/openapi.json and browsed through the Scalar reference page the SPA
// serves at /docs. The parity test in this package fails when a route is
// registered in cmd/server/main.go without a matching entry in the
// document (or vice versa), so the two change together.
package apidocs

import (
	_ "embed"
	"fmt"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var specYAML []byte

var (
	specOnce sync.Once
	specDoc  map[string]any
	specErr  error
)

// Spec returns the parsed OpenAPI document.
func Spec() (map[string]any, error) {
	specOnce.Do(func() {
		if err := yaml.Unmarshal(specYAML, &specDoc); err != nil {
			specErr = fmt.Errorf("apidocs: parse openapi.yaml: %w", err)
		}
	})
	return specDoc, specErr
}

// SpecHandler serves the document as JSON at /api/openapi.json.
func SpecHandler(c echo.Context) error {
	doc, err := Spec()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, doc)
}

// DocsRedirect sends /api/docs visitors to the Scalar page at /docs.
func DocsRedirect(c echo.Context) error {
	return c.Redirect(http.StatusFound, "/docs")
}
