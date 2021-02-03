// +build debugspy

package debugspy

import (
	"fmt"

	"github.com/taoey/pyroscope/pkg/agent/spy"
)

type DebugSpy struct {
	pid int
}

func Start(pid int) (spy.Spy, error) {
	return &DebugSpy{
		pid: pid,
	}, nil
}

func (s *DebugSpy) Stop() error {
	return nil
}

// Snapshot calls callback function with stack-trace or error.
func (s *DebugSpy) Snapshot(cb func([]byte, error)) {
	stacktrace := fmt.Sprintf("debug_%d;debug", s.pid)
	cb([]byte(stacktrace), nil)
}

func init() {
	spy.RegisterSpy("debugspy", Start)
}
