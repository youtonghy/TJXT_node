package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/go-resty/resty/v2"
	"github.com/youtonghy/TJXT_node/conf"
)

// Panel is the interface for different panel's api.

type Client struct {
	client  *resty.Client
	APIHost string
	Token   string
	NodeId  int
	mode    protocolMode

	// mu guards the mutable protocol and cache state below; the node info
	// monitor and the traffic report task use one client concurrently.
	mu               sync.Mutex
	v3Failed         bool
	nodeEtag         string
	userEtag         string
	responseBodyHash string
}

func (c *Client) getNodeEtag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodeEtag
}

func (c *Client) getUserEtag() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userEtag
}

func (c *Client) setUserEtag(etag string) {
	c.mu.Lock()
	c.userEtag = etag
	c.mu.Unlock()
}

// updateNodeCache records a fetched node config and reports whether its body
// differs from the previous one.
func (c *Client) updateNodeCache(etag string, body []byte) bool {
	hash := sha256.Sum256(body)
	newBodyHash := hex.EncodeToString(hash[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.responseBodyHash == newBodyHash {
		return false
	}
	c.responseBodyHash = newBodyHash
	c.nodeEtag = etag
	return true
}

func New(c *conf.NodeConfig) (*Client, error) {
	client := resty.New()
	retryCount := conf.DefaultNodeRetryCount
	if c.RetryCount != nil {
		retryCount = *c.RetryCount
	}
	client.SetRetryCount(retryCount)
	client.SetHeader("User-Agent", fmt.Sprintf("tjxt-node go-resty/%s", resty.Version))
	client.SetHeader("X-Server-Token", c.Key)
	if c.Timeout > 0 {
		client.SetTimeout(time.Duration(c.Timeout) * time.Second)
	} else {
		client.SetTimeout(time.Duration(conf.DefaultNodeTimeout) * time.Second)
	}
	client.OnError(func(req *resty.Request, err error) {
		var v *resty.ResponseError
		if errors.As(err, &v) {
			logrus.Error(v.Err)
		}
	})
	client.SetBaseURL(c.APIHost)
	// The node key travels only in the X-Server-Token header, never in the URL
	// where proxies and access logs would record it.
	client.SetQueryParams(map[string]string{
		"node_type": "v3ui",
		"node_id":   strconv.Itoa(c.NodeID),
	})
	return &Client{
		client:  client,
		Token:   c.Key,
		APIHost: c.APIHost,
		NodeId:  c.NodeID,
		mode:    parseProtocolMode(c.Protocol),
	}, nil
}
