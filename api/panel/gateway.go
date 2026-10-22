package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-resty/resty/v2"
)

type protocolMode string

const (
	modeAuto protocolMode = "auto"
	modeV3   protocolMode = "v3ui"
	modeV1   protocolMode = "v1"
)

type gatewayEnvelope struct {
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

func parseProtocolMode(raw string) protocolMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "v3", "v3ui":
		return modeV3
	case "v1", "uniproxy":
		return modeV1
	default:
		return modeAuto
	}
}

func (c *Client) useV3() bool {
	return c.mode == modeAuto || c.mode == modeV3
}

func (c *Client) useV1() bool {
	return c.mode == modeAuto || c.mode == modeV1
}

func (c *Client) markV3Unavailable() {
	if c.mode == modeAuto {
		c.mu.Lock()
		c.v3Failed = true
		c.mu.Unlock()
	}
}

func (c *Client) v3Available() bool {
	if !c.useV3() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.v3Failed
}

func (c *Client) callV3(
	ctx context.Context,
	method string,
	endpoint string,
	params any,
	ifNoneMatch string,
) (*resty.Response, []byte, error) {
	payload := map[string]any{
		"endpoint": endpoint,
		"method":   method,
		"params":   params,
	}
	req := c.client.R().
		SetContext(ctx).
		SetHeader("X-Server-Token", c.Token).
		SetHeader("Content-Type", "application/json").
		SetBody(payload)
	if ifNoneMatch != "" {
		req.SetHeader("If-None-Match", ifNoneMatch)
	}
	r, err := req.Post("/api/v3/server")
	if err != nil {
		return nil, nil, err
	}
	if r == nil {
		return nil, nil, fmt.Errorf("nil v3 gateway response")
	}
	if r.StatusCode() == http.StatusNotModified {
		return r, nil, nil
	}
	if r.StatusCode() == http.StatusNotFound || isV3Unsupported(r) {
		c.markV3Unavailable()
		return r, nil, errV3Unsupported
	}
	if r.StatusCode() >= 400 {
		return r, nil, newStatusError("v3 gateway", r)
	}
	var env gatewayEnvelope
	if err := json.Unmarshal(r.Body(), &env); err != nil {
		return r, nil, fmt.Errorf("decode v3 gateway envelope: %w", err)
	}
	// Panels that do not envelope responses answer with the payload itself; an
	// explicit "data": null is an empty answer, not the payload.
	if len(env.Data) == 0 {
		return r, r.Body(), nil
	}
	if string(env.Data) == "null" {
		return r, nil, nil
	}
	return r, env.Data, nil
}

var errV3Unsupported = fmt.Errorf("v3 gateway does not serve node endpoints")

func isV3Unsupported(r *resty.Response) bool {
	body := strings.ToLower(string(r.Body()))
	return strings.Contains(body, "not available through gateway") ||
		strings.Contains(body, "invalid endpoint") && strings.Contains(body, "server")
}

// StatusError is a panel answer with a non-2xx HTTP status.
type StatusError struct {
	Action string
	Code   int
	Status string
	Body   string
	// ErrorCode is the body's "error_code", e.g. "node_unauthorized".
	ErrorCode string
}

// errorCodeNodeUnauthorized is the panel's answer (HTTP 400, V2Board style) to
// a node key it does not accept.
const errorCodeNodeUnauthorized = "node_unauthorized"

func newStatusError(action string, r *resty.Response) *StatusError {
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	// Not every error body is JSON; the code is optional.
	_ = json.Unmarshal(r.Body(), &body)
	return &StatusError{
		Action:    action,
		Code:      r.StatusCode(),
		Status:    r.Status(),
		Body:      truncateBody(r.Body()),
		ErrorCode: body.ErrorCode,
	}
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Action, e.Status, e.Body)
}

// Unauthorized reports whether the panel rejected the node key: HTTP 401/403,
// or any status whose body carries "error_code": "node_unauthorized".
func (e *StatusError) Unauthorized() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden ||
		e.ErrorCode == errorCodeNodeUnauthorized
}

// IsUnauthorized reports whether err is the panel rejecting the node key.
func IsUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Unauthorized()
}

// Permanent reports whether resending the same request cannot succeed: the panel
// rejected its content. Auth failures, missing endpoints, timeouts and rate
// limits are retryable.
func (e *StatusError) Permanent() bool {
	if e.Unauthorized() {
		// A rotated key is fixed by the operator; the request itself is fine.
		return false
	}
	switch e.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return e.Code >= 400 && e.Code < 500
}

func truncateBody(body []byte) string {
	if len(body) > 240 {
		return string(body[:240])
	}
	return string(body)
}

// identityParams identifies the node to the v3 gateway. The token is not part
// of it: the gateway forwards the X-Server-Token header to the node routes.
// Without a configured NodeID the node is paired by key: the panel finds the
// node from its own key, so no id is sent.
func identityParams(c *Client) map[string]any {
	p := map[string]any{"node_type": "v3ui"}
	if c.NodeId > 0 {
		p["node_id"] = c.NodeId
		p["server_id"] = c.NodeId
	}
	return p
}
