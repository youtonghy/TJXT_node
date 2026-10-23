package dispatcher

import (
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/youtonghy/TJXT_node/limiter"
)

// activityWriter marks the device of a connection active whenever data flows
// through it, so a long-lived connection keeps its device online.
type activityWriter struct {
	buf.Writer
	activity *limiter.Activity
}

func (w *activityWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.activity.Touch()
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *activityWriter) Close() error {
	return common.Close(w.Writer)
}

func (w *activityWriter) Interrupt() {
	common.Interrupt(w.Writer)
}

// withActivity wraps writer when the connection's device is tracked.
func withActivity(writer buf.Writer, activity *limiter.Activity) buf.Writer {
	if activity == nil {
		return writer
	}
	return &activityWriter{Writer: writer, activity: activity}
}
