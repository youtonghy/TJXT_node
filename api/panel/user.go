package panel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/go-resty/resty/v2"
	"github.com/vmihailenco/msgpack/v5"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id" msgpack:"id"`
	Uuid        string `json:"uuid" msgpack:"uuid"`
	SpeedLimit  int    `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit int    `json:"device_limit" msgpack:"device_limit"`
}

type AliveMap struct {
	Alive map[int]int `json:"alive"`
}

// GetUserList pulls the user list from the panel. It returns nil (not an empty
// slice) when the list is unchanged since the last call (HTTP 304), and a non-nil,
// possibly empty slice when the panel sent a list.
func (c *Client) GetUserList(ctx context.Context) ([]UserInfo, error) {
	if c.v3Available() {
		users, err := c.getUserListV3(ctx)
		if err == nil || !errors.Is(err, errV3Unsupported) {
			return users, err
		}
	}
	if !c.useV1() {
		return nil, fmt.Errorf("v3 user list failed and v1 fallback is disabled")
	}
	return c.getUserListV1(ctx)
}

func (c *Client) getUserListV3(ctx context.Context) ([]UserInfo, error) {
	r, body, err := c.callV3(ctx, "GET", "server/UniProxy/user", identityParams(c), c.getUserEtag())
	if err != nil {
		return nil, err
	}
	if r.StatusCode() == http.StatusNotModified {
		return nil, nil
	}
	users, err := decodeUserListJSON(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decode v3 user list: %w", err)
	}
	c.setUserEtag(r.Header().Get("ETag"))
	return users, nil
}

func (c *Client) getUserListV1(ctx context.Context) ([]UserInfo, error) {
	const path = "/api/v1/server/UniProxy/user"
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader("If-None-Match", c.getUserEtag()).
		SetHeader("X-Response-Format", "msgpack").
		SetDoNotParseResponse(true).
		Get(path)
	if err != nil {
		return nil, err
	}
	if r == nil || r.RawResponse == nil {
		return nil, fmt.Errorf("received nil response or raw response")
	}
	defer r.RawResponse.Body.Close()

	if r.StatusCode() == http.StatusNotModified {
		return nil, nil
	}
	if r.StatusCode() < 200 || r.StatusCode() >= 300 {
		body, _ := io.ReadAll(io.LimitReader(r.RawResponse.Body, 240))
		return nil, fmt.Errorf("get user list: %s: %s", r.Status(), body)
	}
	var users []UserInfo
	if isMsgpack(r.Header().Get("Content-Type")) {
		users, err = decodeUserListMsgpack(r.RawResponse.Body)
	} else {
		users, err = decodeUserListJSON(r.RawResponse.Body)
	}
	if err != nil {
		return nil, fmt.Errorf("decode user list error: %w", err)
	}
	c.setUserEtag(r.Header().Get("ETag"))
	return users, nil
}

// isMsgpack reports whether a Content-Type is msgpack; the panel answers with
// application/msgpack, older panels with application/x-msgpack.
func isMsgpack(contentType string) bool {
	return strings.Contains(contentType, "application/msgpack") ||
		strings.Contains(contentType, "application/x-msgpack")
}

var errMissingUsers = errors.New(`response has no "users" array`)

// decodeUserListMsgpack decodes {"users": [...]} and returns a non-nil slice.
func decodeUserListMsgpack(r io.Reader) ([]UserInfo, error) {
	var body struct {
		Users *[]UserInfo `msgpack:"users"`
	}
	if err := msgpack.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	if body.Users == nil || *body.Users == nil {
		return nil, errMissingUsers
	}
	return *body.Users, nil
}

// decodeUserListJSON streams {"users": [...]} without buffering the whole list.
// Only the top-level "users" member is used; other members are skipped even if
// their value is the string "users". An empty array yields a non-nil empty slice.
func decodeUserListJSON(r io.Reader) ([]UserInfo, error) {
	dec := jsontext.NewDecoder(r)
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	if tok.Kind() != '{' {
		return nil, fmt.Errorf("expected a JSON object, got %v", tok.Kind())
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		if key.String() != "users" {
			if err := dec.SkipValue(); err != nil {
				return nil, err
			}
			continue
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		if tok.Kind() != '[' {
			return nil, errMissingUsers
		}
		users := make([]UserInfo, 0)
		for dec.PeekKind() != ']' {
			val, err := dec.ReadValue()
			if err != nil {
				return nil, fmt.Errorf("read user object: %w", err)
			}
			var u UserInfo
			if err := json.Unmarshal(val, &u); err != nil {
				return nil, fmt.Errorf("unmarshal user error: %w", err)
			}
			users = append(users, u)
		}
		return users, nil
	}
	return nil, errMissingUsers
}

// GetUserAlive fetches the panel-wide alive IP count per user.
func (c *Client) GetUserAlive(ctx context.Context) (map[int]int, error) {
	const path = "/api/v1/server/UniProxy/alivelist"
	r, err := c.client.R().
		SetContext(ctx).
		ForceContentType("application/json").
		Get(path)
	if err := checkResponse(r, err, "get alive list"); err != nil {
		return nil, err
	}
	alive := &AliveMap{}
	if err := json.Unmarshal(r.Body(), alive); err != nil {
		return nil, fmt.Errorf("unmarshal user alive list error: %w", err)
	}
	if alive.Alive == nil {
		alive.Alive = make(map[int]int)
	}
	return alive.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// TrafficBatchHeader carries the traffic batch ID on direct (v1) pushes. The v3
// gateway only forwards auth headers, so there the ID travels in the body.
const TrafficBatchHeader = "X-Traffic-Batch-Id"

// PushResult is the panel's answer to an accepted traffic push.
type PushResult struct {
	// RejectedUserIDs lists users the panel accepted but did not bill (no
	// package on this node); their traffic is not retryable.
	RejectedUserIDs []int `json:"rejected_user_ids"`
	// RetrySafe reports whether the panel deduplicates this batch by its ID.
	RetrySafe bool `json:"retry_safe"`
}

// trafficPushBody is the wrapped push payload accepted by the panel:
// {"batch_id": "...", "traffic": {"<uid>": [upload, download]}}.
type trafficPushBody struct {
	BatchID string              `json:"batch_id"`
	Traffic map[string][2]int64 `json:"traffic"`
}

func newTrafficPushBody(batchID string, userTraffic []UserTraffic) trafficPushBody {
	data := make(map[string][2]int64, len(userTraffic))
	for i := range userTraffic {
		key := strconv.Itoa(userTraffic[i].UID)
		pair := data[key]
		pair[0] += userTraffic[i].Upload
		pair[1] += userTraffic[i].Download
		data[key] = pair
	}
	return trafficPushBody{BatchID: batchID, Traffic: data}
}

func decodePushResult(body []byte) (*PushResult, error) {
	result := &PushResult{}
	if len(body) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(body, result); err != nil {
		return nil, fmt.Errorf("decode push response: %w", err)
	}
	return result, nil
}

// ReportUserTraffic reports one traffic batch. The batch ID lets the panel apply a
// retried batch exactly once, so callers must resend the same ID and content until
// this returns nil.
func (c *Client) ReportUserTraffic(ctx context.Context, batchID string, userTraffic []UserTraffic) (*PushResult, error) {
	body := newTrafficPushBody(batchID, userTraffic)
	if c.v3Available() {
		params := identityParams(c)
		params["batch_id"] = body.BatchID
		params["traffic"] = body.Traffic
		r, _, err := c.callV3(ctx, "POST", "server/UniProxy/push", params, "")
		if err == nil {
			return decodePushResult(r.Body())
		}
		if !errors.Is(err, errV3Unsupported) {
			return nil, err
		}
	}
	if !c.useV1() {
		return nil, fmt.Errorf("v3 push failed and v1 fallback is disabled")
	}
	const path = "/api/v1/server/UniProxy/push"
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader(TrafficBatchHeader, batchID).
		SetBody(body).
		ForceContentType("application/json").
		Post(path)
	if err := checkResponse(r, err, "push traffic"); err != nil {
		return nil, err
	}
	return decodePushResult(r.Body())
}

func (c *Client) ReportNodeOnlineUsers(ctx context.Context, data *map[int][]string) error {
	if c.v3Available() {
		params := identityParams(c)
		if data != nil {
			params["users"] = *data
		}
		_, _, err := c.callV3(ctx, "POST", "server/UniProxy/alive", params, "")
		if err == nil || !errors.Is(err, errV3Unsupported) {
			return err
		}
	}
	if !c.useV1() {
		return fmt.Errorf("v3 alive failed and v1 fallback is disabled")
	}
	const path = "/api/v1/server/UniProxy/alive"
	r, err := c.client.R().
		SetContext(ctx).
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	return checkResponse(r, err, "report online users")
}

// checkResponse turns a transport error or a non-2xx status into an error.
func checkResponse(r *resty.Response, err error, action string) error {
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%s: nil response", action)
	}
	if r.StatusCode() < 200 || r.StatusCode() >= 300 {
		return newStatusError(action, r)
	}
	return nil
}
