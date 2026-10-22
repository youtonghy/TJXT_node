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
	// NodeId is the configured node id; 0 pairs by key (see learnedID).
	NodeId int
	mode   protocolMode

	// mu guards the mutable protocol and cache state below; the node info
	// monitor and the traffic report task use one client concurrently.
	mu               sync.Mutex
	v3Failed         bool
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	// learnedID is the panel's base_config.node_id seen when pairing by key.
	// It is used for tags and logs only: requests keep identifying the node by
	// its key, so a node moved to another id by the panel still works.
	learnedID int
}

// EffectiveNodeID returns the configured node id, or the one the panel
// reported for a node paired by key (0 before the first config).
func (c *Client) EffectiveNodeID() int {
	if c.NodeId > 0 {
		return c.NodeId
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.learnedID
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

func bodyHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

// nodeConfigChanged reports whether a fetched node config differs from the
// last accepted one.
func (c *Client) nodeConfigChanged(body []byte) bool {
	h := bodyHash(body)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.responseBodyHash != h
}

// commitNodeCache records an accepted node config. Only configs that parsed
// are recorded, so a rejected one is reported again on the next fetch instead
// of reading as "unchanged".
func (c *Client) commitNodeCache(etag string, body []byte) {
	h := bodyHash(body)
	c.mu.Lock()
	c.responseBodyHash = h
	c.nodeEtag = etag
	c.mu.Unlock()
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
	// where proxies and access logs would record it. Without a NodeID the panel
	// identifies the node by its own key.
	params := map[string]string{"node_type": "v3ui"}
	if c.NodeID > 0 {
		params["node_id"] = strconv.Itoa(c.NodeID)
	}
	client.SetQueryParams(params)
	return &Client{
		client:  client,
		Token:   c.Key,
		APIHost: c.APIHost,
		NodeId:  c.NodeID,
		mode:    parseProtocolMode(c.Protocol),
	}, nil
}
