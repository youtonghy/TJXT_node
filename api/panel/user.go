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

// userPageSize is the user list page size. v3 asks for it from the first page:
// the panel's v3 gateway refuses responses over 1 MiB, which a whole list of a
// large node exceeds. v1 asks for it once a panel answers with only part of the
// list (a non-null next_cursor).
const userPageSize = 500

// maxUserPages bounds the pages read for one list so a panel that keeps returning
// a cursor cannot keep the node fetching forever.
const maxUserPages = 10_000

func (c *Client) getUserListV3(ctx context.Context) ([]UserInfo, error) {
	params := identityParams(c)
	params["limit"] = userPageSize
	r, body, err := c.callV3(ctx, "GET", "server/UniProxy/user", params, c.getUserEtag())
	if err != nil {
		return nil, err
	}
	if r.StatusCode() == http.StatusNotModified {
		return nil, nil
	}
	page, err := decodeUserListJSON(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decode v3 user list: %w", err)
	}
	users, err := c.readRemainingUsers(ctx, page, c.getUserPageV3)
	if err != nil {
		return nil, err
	}
	c.setUserEtag(listEtag(r.Header().Get("ETag"), page))
	return users, nil
}

func (c *Client) getUserPageV3(ctx context.Context, afterID int64) (userListPage, error) {
	params := identityParams(c)
	params["limit"] = userPageSize
	params["after_id"] = afterID
	_, body, err := c.callV3(ctx, "GET", "server/UniProxy/user", params, "")
	if err != nil {
		return userListPage{}, err
	}
	page, err := decodeUserListJSON(bytes.NewReader(body))
	if err != nil {
		return userListPage{}, fmt.Errorf("decode v3 user page: %w", err)
	}
	return page, nil
}

// listEtag is the ETag to send with the next list request. The panel's ETag
// covers only the page it answers, so it stands for the whole list only when
// the first page is the whole list; a paged list is fetched in full every time.
func listEtag(etag string, first userListPage) string {
	if first.NextCursor != nil {
		return ""
	}
	return etag
}

// readRemainingUsers appends the pages after first while the panel returns a
// cursor. Taking only the first page would silently drop every later user.
func (c *Client) readRemainingUsers(
	ctx context.Context,
	first userListPage,
	next func(context.Context, int64) (userListPage, error),
) ([]UserInfo, error) {
	users := first.Users
	cursor := first.NextCursor
	for pages := 1; cursor != nil; pages++ {
		if pages >= maxUserPages {
			return nil, fmt.Errorf("user list has more than %d pages", maxUserPages)
		}
		page, err := next(ctx, *cursor)
		if err != nil {
			return nil, fmt.Errorf("get user page after %d: %w", *cursor, err)
		}
		if page.NextCursor != nil && *page.NextCursor <= *cursor {
			return nil, fmt.Errorf("user list cursor did not advance past %d", *cursor)
		}
		users = append(users, page.Users...)
		cursor = page.NextCursor
	}
	return users, nil
}

const userListPathV1 = "/api/v1/server/UniProxy/user"

func (c *Client) getUserListV1(ctx context.Context) ([]UserInfo, error) {
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader("If-None-Match", c.getUserEtag()).
		SetHeader("X-Response-Format", "msgpack").
		SetDoNotParseResponse(true).
		Get(userListPathV1)
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
	page, err := decodeUserListResponse(r)
	if err != nil {
		return nil, err
	}
	users, err := c.readRemainingUsers(ctx, page, c.getUserPageV1)
	if err != nil {
		return nil, err
	}
	c.setUserEtag(listEtag(r.Header().Get("ETag"), page))
	return users, nil
}

func (c *Client) getUserPageV1(ctx context.Context, afterID int64) (userListPage, error) {
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader("X-Response-Format", "msgpack").
		SetQueryParam("limit", strconv.Itoa(userPageSize)).
		SetQueryParam("after_id", strconv.FormatInt(afterID, 10)).
		SetDoNotParseResponse(true).
		Get(userListPathV1)
	if err != nil {
		return userListPage{}, err
	}
	if r == nil || r.RawResponse == nil {
		return userListPage{}, fmt.Errorf("received nil response or raw response")
	}
	defer r.RawResponse.Body.Close()
	return decodeUserListResponse(r)
}

// decodeUserListResponse decodes a 2xx user list response body (msgpack or JSON).
func decodeUserListResponse(r *resty.Response) (userListPage, error) {
	if r.StatusCode() < 200 || r.StatusCode() >= 300 {
		body, _ := io.ReadAll(io.LimitReader(r.RawResponse.Body, 240))
		return userListPage{}, fmt.Errorf("get user list: %s: %s", r.Status(), body)
	}
	var page userListPage
	var err error
	if isMsgpack(r.Header().Get("Content-Type")) {
		page, err = decodeUserListMsgpack(r.RawResponse.Body)
	} else {
		page, err = decodeUserListJSON(r.RawResponse.Body)
	}
	if err != nil {
		return userListPage{}, fmt.Errorf("decode user list error: %w", err)
	}
	return page, nil
}

// isMsgpack reports whether a Content-Type is msgpack; the panel answers with
// application/msgpack, older panels with application/x-msgpack.
func isMsgpack(contentType string) bool {
	return strings.Contains(contentType, "application/msgpack") ||
		strings.Contains(contentType, "application/x-msgpack")
}

var errMissingUsers = errors.New(`response has no "users" array`)

// userListPage is one user list response. NextCursor is set when the panel sent
// only part of the list; the rest follows with after_id=NextCursor.
type userListPage struct {
	Users      []UserInfo
	NextCursor *int64
}

// decodeUserListMsgpack decodes {"users": [...], "next_cursor": n|null}; Users is
// a non-nil slice.
func decodeUserListMsgpack(r io.Reader) (userListPage, error) {
	var body struct {
		Users      *[]UserInfo `msgpack:"users"`
		NextCursor *int64      `msgpack:"next_cursor"`
	}
	if err := msgpack.NewDecoder(r).Decode(&body); err != nil {
		return userListPage{}, err
	}
	if body.Users == nil || *body.Users == nil {
		return userListPage{}, errMissingUsers
	}
	return userListPage{Users: *body.Users, NextCursor: body.NextCursor}, nil
}

// decodeUserListJSON streams {"users": [...], "next_cursor": n|null} without
// buffering the whole list. Only top-level members are used; other members are
// skipped even if their value is the string "users". An empty array yields a
// non-nil empty slice.
func decodeUserListJSON(r io.Reader) (userListPage, error) {
	dec := jsontext.NewDecoder(r)
	tok, err := dec.ReadToken()
	if err != nil {
		return userListPage{}, err
	}
	if tok.Kind() != '{' {
		return userListPage{}, fmt.Errorf("expected a JSON object, got %v", tok.Kind())
	}
	var page userListPage
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return userListPage{}, err
		}
		switch key.String() {
		case "users":
			if page.Users, err = decodeUserArray(dec); err != nil {
				return userListPage{}, err
			}
		case "next_cursor":
			val, err := dec.ReadValue()
			if err != nil {
				return userListPage{}, err
			}
			if err := json.Unmarshal(val, &page.NextCursor); err != nil {
				return userListPage{}, fmt.Errorf("decode next_cursor: %w", err)
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return userListPage{}, err
			}
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return userListPage{}, err
	}
	if page.Users == nil {
		return userListPage{}, errMissingUsers
	}
	return page, nil
}

func decodeUserArray(dec *jsontext.Decoder) ([]UserInfo, error) {
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
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	return users, nil
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
	// RevokedUserIDs lists users who just lost access to this node (quota used
	// up, package ended); the node disconnects them without waiting for its next
	// user list poll.
	RevokedUserIDs []int `json:"revoked_user_ids"`
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

// maxPushEntries is the most users the panel accepts in one push.
const maxPushEntries = 2000

// ReportUserTraffic reports one traffic batch. The batch ID lets the panel apply a
// retried batch exactly once, so callers must resend the same ID and content until
// this returns nil. A batch with more users than one push may carry is sent in
// parts with IDs "<batchID>.<n>"; the parts depend only on the batch content, so a
// retry resends parts the panel already applied under the same IDs.
func (c *Client) ReportUserTraffic(ctx context.Context, batchID string, userTraffic []UserTraffic) (*PushResult, error) {
	if len(userTraffic) <= maxPushEntries {
		return c.reportTrafficPart(ctx, batchID, userTraffic)
	}
	merged := &PushResult{RetrySafe: true}
	for part, start := 0, 0; start < len(userTraffic); part, start = part+1, start+maxPushEntries {
		end := min(start+maxPushEntries, len(userTraffic))
		result, err := c.reportTrafficPart(ctx, fmt.Sprintf("%s.%d", batchID, part), userTraffic[start:end])
		if err != nil {
			return nil, err
		}
		merged.RejectedUserIDs = append(merged.RejectedUserIDs, result.RejectedUserIDs...)
		merged.RevokedUserIDs = append(merged.RevokedUserIDs, result.RevokedUserIDs...)
		merged.RetrySafe = merged.RetrySafe && result.RetrySafe
	}
	return merged, nil
}

func (c *Client) reportTrafficPart(ctx context.Context, batchID string, userTraffic []UserTraffic) (*PushResult, error) {
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

// ReportNodeOnlineUsers reports the online IPs per user. The panel replaces the
// node's previous report, so an empty (or nil) map clears it.
func (c *Client) ReportNodeOnlineUsers(ctx context.Context, data *map[int][]string) error {
	if data == nil || *data == nil {
		data = &map[int][]string{}
	}
	if c.v3Available() {
		params := identityParams(c)
		params["users"] = *data
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
