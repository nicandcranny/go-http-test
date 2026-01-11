package httptest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ResponseWriter is a struct that handles the response writing.
type ResponseWriter struct {
	w http.ResponseWriter
}

// SetBodyBytes sets the response body.
func (r *ResponseWriter) SetBodyBytes(b []byte) (int, error) {
	return r.w.Write(b)
}

// SetBodyJSON marshals s to JSON and sets it as the response body, and
// sets the Content-Type header to application/json.
func (r *ResponseWriter) SetBodyJSON(s any) (int, error) {
	r.w.Header().Set("Content-Type", "application/json")

	b, err := json.Marshal(&s)
	if err != nil {
		return 0, fmt.Errorf("json.Marshal: %w", err)
	}

	return r.w.Write(b)
}

// SetStatusCode sets the response status code.
func (r *ResponseWriter) SetStatusCode(statusCode int) {
	r.w.WriteHeader(statusCode)
}

// Header returns the response headers.
func (r *ResponseWriter) Header() http.Header {
	return r.w.Header()
}

// JSON sends a JSON response with status code.
func (r *ResponseWriter) JSON(code int, i any) error {
	r.w.Header().Set("Content-Type", "application/json")
	r.w.WriteHeader(code)
	return json.NewEncoder(r.w).Encode(i)
}

// HTML sends an HTML response with status code.
func (r *ResponseWriter) HTML(code int, html string) error {
	r.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	r.w.WriteHeader(code)
	_, err := r.w.Write([]byte(html))
	return err
}

// String sends a string response with status code.
func (r *ResponseWriter) String(code int, s string) error {
	r.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.w.WriteHeader(code)
	_, err := r.w.Write([]byte(s))
	return err
}

// Blob sends a blob response with status code and content type.
func (r *ResponseWriter) Blob(code int, contentType string, b []byte) error {
	r.w.Header().Set("Content-Type", contentType)
	r.w.WriteHeader(code)
	_, err := r.w.Write(b)
	return err
}

// NoContent sends a response with no body and a status code.
func (r *ResponseWriter) NoContent(code int) error {
	r.w.WriteHeader(code)
	return nil
}
