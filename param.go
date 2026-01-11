package httptest

import (
	"github.com/labstack/echo/v4"
)

// Params is a struct to get the request path param.
// It is still here just to keep backwards compatibility.
type Params struct {
	echoContext echo.Context
}

// ByName returns the value of the first Param which key matches the given name.
// If no matching Param is found, an empty string is returned.
func (p Params) ByName(name string) string {
	return p.echoContext.Param(name)
}

func (p Params) All() map[string]string {
	params := make(map[string]string)

	names := p.echoContext.ParamNames()
	values := p.echoContext.ParamValues()

	for i, name := range names {
		if i < len(values) {
			params[name] = values[i]
		}
	}

	return params
}
