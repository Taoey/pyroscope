package agent

import (
	"sync"
	"time"

	// revive:disable:blank-imports Depending on configuration these packages may or may not be used.
	//   That's why we do a blank import here and then packages themselves register with the rest of the code.

	_ "github.com/taoey/pyroscope/pkg/agent/debugspy"
	_ "github.com/taoey/pyroscope/pkg/agent/gospy"
	_ "github.com/taoey/pyroscope/pkg/agent/pyspy"
	_ "github.com/taoey/pyroscope/pkg/agent/rbspy"
	"github.com/taoey/pyroscope/pkg/agent/upstream"
	"github.com/sirupsen/logrus"

	// revive:enable:blank-imports

	"github.com/mitchellh/go-ps"
	"github.com/taoey/pyroscope/pkg/agent/spy"
	"github.com/taoey/pyroscope/pkg/structs/transporttrie"
)

type ProfileSession struct {
	upstream         upstream.Upstream
	appName          string
	spyName          string
	sampleRate       int
	pids             []int
	spies            []spy.Spy
	stopCh           chan struct{}
	trieMutex        sync.Mutex
	trie             *transporttrie.Trie
	withSubprocesses bool

	startTime time.Time
	stopTime  time.Time
}

func NewSession(upstream upstream.Upstream, appName string, spyName string, sampleRate int, pid int, withSubprocesses bool) *ProfileSession {
	return &ProfileSession{
		upstream:         upstream,
		appName:          appName,
		spyName:          spyName,
		sampleRate:       sampleRate,
		pids:             []int{pid},
		stopCh:           make(chan struct{}),
		withSubprocesses: withSubprocesses,
	}
}

func (ps *ProfileSession) takeSnapshots() {
	// TODO: has to be configurable
	ticker := time.NewTicker(time.Second / time.Duration(ps.sampleRate))
	for {
		select {
		case <-ticker.C:
			if ps.isDueForReset() {
				ps.reset()
			}
			for _, spy := range ps.spies {
				spy.Snapshot(func(stack []byte, err error) {
					if stack != nil && len(stack) > 0 {
						ps.trieMutex.Lock()
						defer ps.trieMutex.Unlock()

						ps.trie.Insert(stack, 1, true)
					}
				})
			}
		case <-ps.stopCh:
			ticker.Stop()
			for _, spy := range ps.spies {
				spy.Stop()
			}

			return
		}
	}
}

func (ps *ProfileSession) Start() error {
	ps.reset()

	s, err := spy.SpyFromName(ps.spyName, ps.pids[0])
	if err != nil {
		return err
	}

	ps.spies = append(ps.spies, s)
	go ps.takeSnapshots()
	return nil
}

func (ps *ProfileSession) isDueForReset() bool {
	// TODO: duration should be either taken from config or ideally passed from server
	dur := 10 * time.Second
	now := time.Now().Truncate(dur)
	st := ps.startTime.Truncate(dur)

	return !st.Equal(now)
}

// the difference between stop and reset is that reset stops current session
//   and then instantly starts a new one
func (ps *ProfileSession) reset() {
	ps.trieMutex.Lock()
	defer ps.trieMutex.Unlock()

	now := time.Now()
	if ps.trie != nil {
		// TODO: duration should be either taken from config or ideally passed from server
		dur := 10 * time.Second
		now = now.Truncate(dur)
		ps.upstream.Upload(ps.appName, ps.startTime, now, ps.spyName, ps.sampleRate, ps.trie)
	}

	ps.startTime = now
	ps.trie = transporttrie.New()

	if ps.withSubprocesses {
		ps.addSubprocesses()
	}
}

func (ps *ProfileSession) Stop() {
	ps.trieMutex.Lock()
	defer ps.trieMutex.Unlock()

	ps.stopTime = time.Now()
	select {
	case ps.stopCh <- struct{}{}:
	default:
	}
	close(ps.stopCh)

	if ps.trie != nil {
		ps.upstream.Upload(ps.appName, ps.startTime, time.Now(), ps.spyName, ps.sampleRate, ps.trie)
	}
}

func (ps *ProfileSession) addSubprocesses() {
	newPids := findAllSubprocesses(ps.pids[0])
	for _, newPid := range newPids {
		if !includes(ps.pids, newPid) {
			ps.pids = append(ps.pids, newPid)
			newSpy, err := spy.SpyFromName(ps.spyName, newPid)
			if err != nil {
				logrus.WithFields(logrus.Fields{
					"spy-name": ps.spyName,
					"pid":      newPid,
				}).Error("failed to initialize a spy")
			} else {
				logrus.WithFields(logrus.Fields{
					"spy-name": ps.spyName,
					"pid":      newPid,
				}).Debug("started spy for subprocess")
				ps.spies = append(ps.spies, newSpy)
			}
		}
	}
}

func includes(arr []int, v int) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}

func findAllSubprocesses(pid int) []int {
	res := []int{}

	childrenLookup := map[int][]int{}
	processes, err := ps.Processes()
	if err != nil {
		// TODO: handle
		return res
	}
	for _, p := range processes {
		ppid := p.PPid()
		if _, ok := childrenLookup[ppid]; !ok {
			childrenLookup[ppid] = []int{}
		}
		childrenLookup[ppid] = append(childrenLookup[ppid], p.Pid())
	}

	todo := []int{pid}
	for len(todo) > 0 {
		parentPid := todo[0]
		todo = todo[1:]

		if children, ok := childrenLookup[parentPid]; ok {
			for _, childPid := range children {
				res = append(res, childPid)
				todo = append(todo, childPid)
			}
		}
	}

	return res
}
