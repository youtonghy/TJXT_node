package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/youtonghy/TJXT_node/api/panel"
)

// Traffic the panel did not accept before the counters holding it went away
// (the node stopping, users being removed) is kept on disk and reported first on
// the next push, under its original batch ID so the panel counts it once.

// spoolDir is where unreported traffic is kept; the service may write only below
// /etc/tjxt-node. TJXT_SPOOL_DIR overrides it (tests, containers).
var spoolDir = func() string {
	if dir := os.Getenv("TJXT_SPOOL_DIR"); dir != "" {
		return dir
	}
	return "/etc/tjxt-node/spool"
}

// spoolMu serializes spool file updates within the process.
var spoolMu sync.Mutex

// spooledBatch is one traffic batch waiting to be reported.
type spooledBatch struct {
	ID      string              `json:"batch_id"`
	Traffic []panel.UserTraffic `json:"traffic"`
}

// spoolPath is the spool file of a node; the tag holds the panel URL, so it is hashed.
func spoolPath(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return filepath.Join(spoolDir(), hex.EncodeToString(sum[:8])+".json")
}

func loadSpool(tag string) ([]spooledBatch, error) {
	data, err := os.ReadFile(spoolPath(tag))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var batches []spooledBatch
	if err := json.Unmarshal(data, &batches); err != nil {
		return nil, fmt.Errorf("decode traffic spool: %w", err)
	}
	return batches, nil
}

// writeSpool replaces the node's spool file atomically; an empty list removes it.
func writeSpool(tag string, batches []spooledBatch) error {
	path := spoolPath(tag)
	if len(batches) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(batches)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spool-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// spoolTraffic appends batches to the node's spool.
func spoolTraffic(tag string, batches ...spooledBatch) error {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	existing, err := loadSpool(tag)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if len(batch.Traffic) > 0 {
			existing = append(existing, batch)
		}
	}
	return writeSpool(tag, existing)
}

// reportSpool reports spooled batches in order and drops each one the panel
// accepts; it stops at the first failure and keeps the rest.
func (c *Controller) reportSpool(ctx context.Context) error {
	spoolMu.Lock()
	defer spoolMu.Unlock()
	batches, err := loadSpool(c.tag)
	if err != nil || len(batches) == 0 {
		return err
	}
	sent := 0
	var reportErr error
	for _, batch := range batches {
		if _, reportErr = c.apiClient.ReportUserTraffic(ctx, batch.ID, batch.Traffic); reportErr != nil {
			var statusErr *panel.StatusError
			if !errors.As(reportErr, &statusErr) || !statusErr.Permanent() {
				break
			}
			// The panel will never take it; keeping it would block every later batch.
			log.WithFields(log.Fields{"tag": c.tag, "batch": batch.ID, "err": reportErr}).
				Error("Panel rejects a spooled traffic batch, dropping it")
			reportErr = nil
		}
		sent++
	}
	if sent > 0 {
		log.WithFields(log.Fields{"tag": c.tag, "batches": sent}).Info("Reported spooled traffic")
		if err := writeSpool(c.tag, batches[sent:]); err != nil {
			return err
		}
	}
	return reportErr
}
