package exec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/mitchellh/go-ps"
	"github.com/taoey/pyroscope/pkg/agent"
	"github.com/taoey/pyroscope/pkg/agent/spy"
	"github.com/taoey/pyroscope/pkg/agent/upstream/remote"
	"github.com/taoey/pyroscope/pkg/config"
	"github.com/taoey/pyroscope/pkg/util/atexit"
	"github.com/taoey/pyroscope/pkg/util/names"
	"github.com/sirupsen/logrus"
)

func Cli(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("no arguments passed")
	}

	spyName := cfg.Exec.SpyName
	if spyName == "auto" {
		baseName := path.Base(args[0])
		spyName = spy.ResolveAutoName(baseName)
		if spyName == "" {
			supportedSpies := spy.SupportedExecSpies()
			suggestedCommand := fmt.Sprintf("pyroscope exec -spy-name %s %s", supportedSpies[0], strings.Join(args, " "))
			return fmt.Errorf(
				"could not automatically find a spy for program \"%s\". Pass spy name via %s argument, for example: \n  %s\n\nAvailable spies are: %s\n%s\nIf you believe this is a mistake, please submit an issue at %s",
				baseName,
				color.YellowString("-spy-name"),
				color.YellowString(suggestedCommand),
				strings.Join(supportedSpies, ","),
				armMessage(),
				color.BlueString("https://github.com/taoey/pyroscope/issues"),
			)
		}
	}

	logrus.Info("to disable logging from pyroscope, pass " + color.YellowString("-no-logging") + " argument to pyroscope exec")

	if err := performChecks(spyName); err != nil {
		return err
	}

	if cfg.Exec.ApplicationName == "" {
		logrus.Infof("we recommend specifying application name via %s flag or env variable %s", color.YellowString("-application-name"), color.YellowString("PYROSCOPE_APPLICATION_NAME"))
		cfg.Exec.ApplicationName = spyName + "." + names.GetRandomName(generateSeed(args))
		logrus.Infof("for now we chose the name for you and it's \"%s\"", color.BlueString(cfg.Exec.ApplicationName))
	}

	logrus.WithFields(logrus.Fields{
		"args": fmt.Sprintf("%q", args),
	}).Debug("starting command")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{}

	// permissions drop
	if isRoot() && !cfg.Exec.NoRootDrop && os.Getenv("SUDO_UID") != "" && os.Getenv("SUDO_GID") != "" {
		creds, err := generateCredentialsDrop()
		if err != nil {
			logrus.Errorf("failed to drop permissions, %q", err)
		} else {
			cmd.SysProcAttr.Credential = creds
		}
	}

	cmd.SysProcAttr.Setpgid = true
	err := cmd.Start()
	if err != nil {
		return err
	}
	u := remote.New(remote.RemoteConfig{
		UpstreamAddress:        cfg.Exec.ServerAddress,
		UpstreamThreads:        cfg.Exec.UpstreamThreads,
		UpstreamRequestTimeout: cfg.Exec.UpstreamRequestTimeout,
	})
	defer u.Stop()

	logrus.WithFields(logrus.Fields{
		"app-name":            cfg.Exec.ApplicationName,
		"spy-name":            spyName,
		"pid":                 cmd.Process.Pid,
		"detect-subprocesses": cfg.Exec.DetectSubprocesses,
	}).Debug("starting agent session")

	// TODO: add sample rate, make it configurable
	sess := agent.NewSession(u, cfg.Exec.ApplicationName, spyName, 100, cmd.Process.Pid, cfg.Exec.DetectSubprocesses)
	err = sess.Start()
	if err != nil {
		logrus.Errorf("error when starting session: %q", err)
	}
	defer sess.Stop()

	waitForProcessToExit(cmd)
	return nil
}

// TODO: very hacky, at some point we'll need to make `cmd.Wait()` work
//   Currently the issue is that on Linux it often thinks the process exited when it did not.
func waitForProcessToExit(cmd *exec.Cmd) {
	sigc := make(chan struct{})

	go func() {
		cmd.Wait()
	}()

	atexit.Register(func() {
		sigc <- struct{}{}
	})

	t := time.NewTicker(time.Second)
	for {
		select {
		case <-sigc:
			logrus.Debug("received a signal, killing subprocess")
			cmd.Process.Kill()
			return
		case <-t.C:
			p, err := ps.FindProcess(cmd.Process.Pid)
			if p == nil || err != nil {
				logrus.WithField("err", err).Debug("could not find subprocess, it might be dead")
				return
			}
		}
	}
}

func performChecks(spyName string) error {
	if spyName == "gospy" {
		return fmt.Errorf("gospy can not profile other processes. See our documentation on using gospy: %s", color.BlueString("https://pyroscope.io/docs/"))
	}

	if runtime.GOOS == "darwin" {
		if !isRoot() {
			logrus.Fatal("on macOS you're required to run the agent with sudo")
		}
	}

	if !stringsContains(spy.SupportedSpies, spyName) {
		supportedSpies := spy.SupportedExecSpies()
		return fmt.Errorf(
			"Spy \"%s\" is not supported. Available spies are: %s\n%s",
			color.BlueString(spyName),
			strings.Join(supportedSpies, ","),
			armMessage(),
		)
	}

	return nil
}

func stringsContains(arr []string, element string) bool {
	for _, v := range arr {
		if v == element {
			return true
		}
	}
	return false
}

func isRoot() bool {
	u, err := user.Current()
	return err == nil && u.Username == "root"
}

func armMessage() string {
	if runtime.GOARCH == "arm64" {
		return "Note that rbspy is not available on arm64 platform"
	}
	return ""
}

func generateSeed(args []string) string {
	path, err := os.Getwd()
	if err != nil {
		path = "<unknown>"
	}
	return path + "|" + strings.Join(args, "&")
}

func generateCredentialsDrop() (*syscall.Credential, error) {
	sudoUser := os.Getenv("SUDO_USER")
	sudoUID := os.Getenv("SUDO_UID")
	sudoGid := os.Getenv("SUDO_GID")

	logrus.Infof("dropping permissions, running command as %q (%s/%s)", sudoUser, sudoUID, sudoGid)

	uid, err := strconv.Atoi(sudoUID)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(sudoGid)
	if err != nil {
		return nil, err
	}

	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}
