package format

import (
	"fmt"
	"strings"
)

// UserTag is a user's email in Xray: the tag of the node the user belongs to
// and the user's credential.
func UserTag(tag string, uuid string) string {
	return fmt.Sprintf("%s|%s", tag, uuid)
}

// NodeTagOf returns the node tag of a UserTag email, or "" for another email.
// Users of nodes that share one inbound keep their own node's tag, so traffic
// and limits are found by it rather than by the inbound.
func NodeTagOf(email string) string {
	if i := strings.LastIndex(email, "|"); i > 0 {
		return email[:i]
	}
	return ""
}
