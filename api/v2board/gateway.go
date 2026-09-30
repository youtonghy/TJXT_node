package panel

import (
	"context"
	"encoding/json"
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
		return r, nil, fmt.Errorf("v3 gateway %s: %s", r.Status(), truncateBody(r.Body()))
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

func truncateBody(body []byte) string {
	if len(body) > 240 {
		return string(body[:240])
	}
	return string(body)
}

func identityParams(c *Client) map[string]any {
	return map[string]any{
		"token":     c.Token,
		"node_id":   c.NodeId,
		"server_id": c.NodeId,
		"node_type": "v3ui",
	}
}
