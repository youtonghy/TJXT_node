package panel

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"encoding/json/jsontext"
	"encoding/json/v2"

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

type UserListBody struct {
	Users []UserInfo `json:"users" msgpack:"users"`
}

type AliveMap struct {
	Alive map[int]int `json:"alive"`
}

// GetUserList will pull user from the panel
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
	r, body, err := c.callV3(ctx, "GET", "server/UniProxy/user", identityParams(c), c.userEtag)
	if err != nil {
		return nil, err
	}
	if r.StatusCode() == 304 {
		return nil, nil
	}
	userlist := &UserListBody{}
	if err := json.Unmarshal(body, userlist); err != nil {
		return nil, fmt.Errorf("decode v3 user list: %w", err)
	}
	c.userEtag = r.Header().Get("ETag")
	return userlist.Users, nil
}

func (c *Client) getUserListV1(ctx context.Context) ([]UserInfo, error) {
	const path = "/api/v1/server/UniProxy/user"
	r, err := c.client.R().
		SetContext(ctx).
		SetHeader("If-None-Match", c.userEtag).
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

	if r.StatusCode() == 304 {
		return nil, nil
	}
	userlist := &UserListBody{}
	if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
		decoder := msgpack.NewDecoder(r.RawResponse.Body)
		if err := decoder.Decode(userlist); err != nil {
			return nil, fmt.Errorf("decode user list error: %w", err)
		}
	} else {
		dec := jsontext.NewDecoder(r.RawResponse.Body)
		for {
			tok, err := dec.ReadToken()
			if err != nil {
				return nil, fmt.Errorf("decode user list error: %w", err)
			}
			if tok.Kind() == '"' && tok.String() == "users" {
				break
			}
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, fmt.Errorf("decode user list error: %w", err)
		}
		if tok.Kind() != '[' {
			return nil, fmt.Errorf(`decode user list error: expected "users" array`)
		}
		for dec.PeekKind() != ']' {
			val, err := dec.ReadValue()
			if err != nil {
				return nil, fmt.Errorf("decode user list error: read user object: %w", err)
			}
			var u UserInfo
			if err := json.Unmarshal(val, &u); err != nil {
				return nil, fmt.Errorf("decode user list error: unmarshal user error: %w", err)
			}
			userlist.Users = append(userlist.Users, u)
		}
	}
	c.userEtag = r.Header().Get("ETag")
	return userlist.Users, nil
}

// GetUserAlive will fetch the alive_ip count for users
func (c *Client) GetUserAlive(ctx context.Context) (map[int]int, error) {
	c.AliveMap = &AliveMap{}
	const path = "/api/v1/server/UniProxy/alivelist"
	r, err := c.client.R().
		SetContext(ctx).
		ForceContentType("application/json").
		Get(path)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		c.AliveMap.Alive = make(map[int]int)
		return c.AliveMap.Alive, nil
	}
	if r == nil || r.RawResponse == nil || r.StatusCode() >= 399 {
		c.AliveMap.Alive = make(map[int]int)
		return c.AliveMap.Alive, nil
	}
	defer r.RawResponse.Body.Close()
	if err := json.Unmarshal(r.Body(), c.AliveMap); err != nil {
		fmt.Printf("unmarshal user alive list error: %s", err)
		c.AliveMap.Alive = make(map[int]int)
	}

	return c.AliveMap.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// ReportUserTraffic reports the user traffic
func (c *Client) ReportUserTraffic(ctx context.Context, userTraffic []UserTraffic) error {
	data := make(map[int][]int64, len(userTraffic))
	for i := range userTraffic {
		data[userTraffic[i].UID] = []int64{userTraffic[i].Upload, userTraffic[i].Download}
	}
	if c.v3Available() {
		params := identityParams(c)
		for uid, pair := range data {
			params[strconv.Itoa(uid)] = pair
		}
		_, _, err := c.callV3(ctx, "POST", "server/UniProxy/push", params, "")
		if err == nil || !errors.Is(err, errV3Unsupported) {
			return err
		}
	}
	if !c.useV1() {
		return fmt.Errorf("v3 push failed and v1 fallback is disabled")
	}
	const path = "/api/v1/server/UniProxy/push"
	_, err := c.client.R().
		SetContext(ctx).
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	return err
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
	_, err := c.client.R().
		SetContext(ctx).
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	return err
}
