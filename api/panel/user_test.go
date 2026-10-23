package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestReportUserTrafficV3WrapsBatch(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":true,"rejected_user_ids":[9],"retry_safe":true}`))
	})
	c := newTestClient(t, p.URL, "v3")
	result, err := c.ReportUserTraffic(context.Background(), "batch-1", []UserTraffic{
		{UID: 1, Upload: 10, Download: 20},
		{UID: 9, Upload: 1, Download: 2},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !result.RetrySafe || len(result.RejectedUserIDs) != 1 || result.RejectedUserIDs[0] != 9 {
		t.Fatalf("unexpected result %+v", result)
	}
	reqs := p.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/api/v3/server" {
		t.Fatalf("unexpected requests %+v", reqs)
	}
	var gateway struct {
		Endpoint string `json:"endpoint"`
		Method   string `json:"method"`
		Params   struct {
			BatchID string              `json:"batch_id"`
			Traffic map[string][2]int64 `json:"traffic"`
			NodeID  int                 `json:"node_id"`
		} `json:"params"`
	}
	if err := json.Unmarshal(reqs[0].Body, &gateway); err != nil {
		t.Fatalf("decode gateway body: %v", err)
	}
	if gateway.Endpoint != "server/UniProxy/push" || gateway.Method != "POST" {
		t.Fatalf("unexpected gateway call %+v", gateway)
	}
	if gateway.Params.BatchID != "batch-1" || gateway.Params.NodeID != 7 {
		t.Fatalf("unexpected params %+v", gateway.Params)
	}
	if got := gateway.Params.Traffic["1"]; got != [2]int64{10, 20} {
		t.Fatalf("unexpected traffic %+v", gateway.Params.Traffic)
	}
}

func TestReportUserTrafficV1SendsBatchHeaderAndBody(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"data":true,"rejected_user_ids":[],"retry_safe":true}`))
	})
	c := newTestClient(t, p.URL, "v1")
	if _, err := c.ReportUserTraffic(context.Background(), "batch-2", []UserTraffic{{UID: 3, Upload: 1, Download: 2}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	req := p.Requests()[0]
	if req.Path != "/api/v1/server/UniProxy/push" {
		t.Fatalf("unexpected path %s", req.Path)
	}
	if got := req.Header.Get(TrafficBatchHeader); got != "batch-2" {
		t.Fatalf("batch header = %q", got)
	}
	var body trafficPushBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.BatchID != "batch-2" || body.Traffic["3"] != [2]int64{1, 2} {
		t.Fatalf("unexpected body %s", req.Body)
	}
}

func TestReportUserTrafficV1FailsOnErrorStatus(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusInternalServerError} {
		p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"Traffic reset in progress; retry this batch"}`))
		})
		c := newTestClient(t, p.URL, "v1")
		if _, err := c.ReportUserTraffic(context.Background(), "b", []UserTraffic{{UID: 1, Upload: 1}}); err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
	}
}

func TestReportUserTrafficV3FailsOnErrorStatus(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"Traffic reset in progress; retry this batch"}`))
	})
	c := newTestClient(t, p.URL, "v3")
	if _, err := c.ReportUserTraffic(context.Background(), "b", []UserTraffic{{UID: 1, Upload: 1}}); err == nil {
		t.Fatal("expected an error on 503")
	}
}

func TestReportNodeOnlineUsersV1FailsOnErrorStatus(t *testing.T) {
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusBadGateway)
	})
	c := newTestClient(t, p.URL, "v1")
	data := map[int][]string{1: {"1.1.1.1"}}
	if err := c.ReportNodeOnlineUsers(context.Background(), &data); err == nil {
		t.Fatal("expected an error on 502")
	}
}

// A batch larger than one push is sent in parts with stable part IDs, so a retry
// after a failed part replays the same parts.
func TestReportUserTrafficSplitsLargeBatches(t *testing.T) {
	fail := true
	p := newFakePanel(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var call struct {
			Params struct {
				BatchID string `json:"batch_id"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		if call.Params.BatchID == "big.1" && fail {
			fail = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":true,"rejected_user_ids":[],"retry_safe":true}`))
	})
	c := newTestClient(t, p.URL, "v3")
	traffic := make([]UserTraffic, maxPushEntries*2+1)
	for i := range traffic {
		traffic[i] = UserTraffic{UID: i + 1, Upload: 1}
	}
	if _, err := c.ReportUserTraffic(context.Background(), "big", traffic); err == nil {
		t.Fatal("a failed part must fail the batch")
	}
	result, err := c.ReportUserTraffic(context.Background(), "big", traffic)
	if err != nil || !result.RetrySafe {
		t.Fatalf("retry: %+v, %v", result, err)
	}
	var ids []string
	for _, req := range p.Requests() {
		var call struct {
			Params struct {
				BatchID string              `json:"batch_id"`
				Traffic map[string][2]int64 `json:"traffic"`
			} `json:"params"`
		}
		if err := json.Unmarshal(req.Body, &call); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(call.Params.Traffic) > maxPushEntries {
			t.Fatalf("part %s has %d entries", call.Params.BatchID, len(call.Params.Traffic))
		}
		ids = append(ids, call.Params.BatchID)
	}
	want := []string{"big.0", "big.1", "big.0", "big.1", "big.2"}
	if len(ids) != len(want) {
		t.Fatalf("part IDs %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("part IDs %v, want %v", ids, want)
		}
	}
}
