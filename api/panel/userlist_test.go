package panel

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestDecodeUserListJSON(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{name: "empty list is explicit", body: `{"users":[]}`, want: 0},
		{name: "users", body: `{"users":[{"id":1,"uuid":"a"},{"id":2,"uuid":"b"}]}`, want: 2},
		{name: "string value users is not the key", body: `{"class":"users","users":[{"id":1,"uuid":"a"}]}`, want: 1},
		{name: "nested users key is ignored", body: `{"meta":{"users":[{"id":9}]},"users":[]}`, want: 0},
		{name: "trailing members", body: `{"users":[{"id":1}],"next_cursor":null}`, want: 1},
		{name: "missing users", body: `{"class":"users"}`, wantErr: true},
		{name: "null users", body: `{"users":null}`, wantErr: true},
		{name: "not an object", body: `["users"]`, wantErr: true},
		{name: "truncated", body: `{"users":[{"id":1}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users, err := decodeUserListJSON(strings.NewReader(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", users)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if users == nil {
				t.Fatal("an explicit list must decode to a non-nil slice")
			}
			if len(users) != tc.want {
				t.Fatalf("got %d users, want %d", len(users), tc.want)
			}
		})
	}
}

func TestGetUserListV1MsgpackEmptyAndNotModified(t *testing.T) {
	empty, err := msgpack.Marshal(map[string]any{"users": []UserInfo{}})
	if err != nil {
		t.Fatal(err)
	}
	p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/msgpack")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(empty)
	})
	c := newTestClient(t, p.URL, "v1")
	users, err := c.GetUserList(context.Background())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("empty msgpack list must be a non-nil empty slice, got %#v", users)
	}
	users, err = c.GetUserList(context.Background())
	if err != nil || users != nil {
		t.Fatalf("304 must return nil, nil; got %#v, %v", users, err)
	}
}

func TestGetUserListV1ErrorStatus(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	c := newTestClient(t, p.URL, "v1")
	if _, err := c.GetUserList(context.Background()); err == nil {
		t.Fatal("expected an error on 500")
	}
}

func TestGetUserListV3EmptyNullAndNotModified(t *testing.T) {
	var answer []byte
	p := newFakePanel(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get("If-None-Match") == `"g"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"g"`)
		_, _ = w.Write(answer)
	})
	c := newTestClient(t, p.URL, "v3")

	answer = []byte(`{"code":200,"data":null}`)
	if _, err := c.GetUserList(context.Background()); err == nil {
		t.Fatal("data: null must be an error, not an unchanged list")
	}

	answer = []byte(`{"code":200,"data":{"users":[],"class":"vless","next_cursor":null}}`)
	users, err := c.GetUserList(context.Background())
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("explicit empty v3 list: %#v, %v", users, err)
	}
	users, err = c.GetUserList(context.Background())
	if err != nil || users != nil {
		t.Fatalf("304 must return nil, nil; got %#v, %v", users, err)
	}
	if !bytes.Contains(p.Requests()[1].Body, []byte(`"server/UniProxy/user"`)) {
		t.Fatalf("unexpected gateway body %s", p.Requests()[1].Body)
	}
}
