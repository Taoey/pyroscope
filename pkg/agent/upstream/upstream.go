package upstream

import (
	"time"

	"github.com/taoey/pyroscope/pkg/structs/transporttrie"
)

type Upstream interface {
	Stop()
	// TODO: too complex, fix it
	Upload(name string, startTime time.Time, endTime time.Time, spyName string, sampleRate int, t *transporttrie.Trie)
}
