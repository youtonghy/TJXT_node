package core

import (
	"errors"
	"fmt"

	"github.com/xtls/xray-core/common"

	panel "github.com/youtonghy/TJXT_node/api/v2board"
)

func (v *V2Core) AddNode(tag string, info *panel.NodeInfo, users []panel.UserInfo) error {
	inBoundConfig, err := buildInbound(info, tag, users)
	if err != nil {
		return fmt.Errorf("build inbound error: %s", err)
	}
	err = v.addInbound(inBoundConfig)
	if err != nil {
		return fmt.Errorf("add inbound error: %s", err)
	}
	return nil
}

// DelNode removes an inbound. Removing an inbound that is not running (or a
// core that has been closed) is not an error, so callers can stop idempotently.
func (v *V2Core) DelNode(tag string) error {
	if v.ihm == nil {
		return nil
	}
	err := v.removeInbound(tag)
	if err != nil && !errors.Is(err, common.ErrNoClue) {
		return fmt.Errorf("remove in error: %s", err)
	}
	return nil
}
