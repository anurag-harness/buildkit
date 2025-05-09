package workers

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/moby/buildkit/util/testutil/integration"
)

func withOTELSocketPath(socketPath string) integration.ConfigUpdater {
	return otelSocketPath(socketPath)
}

type otelSocketPath string

func (osp otelSocketPath) UpdateConfigFile(in string) string {
	return fmt.Sprintf(`%s

[otel]
  socketPath = %q
`, in, osp)
}

func withCDISpecDir(specDir string) integration.ConfigUpdater {
	return cdiSpecDir(specDir)
}

type cdiSpecDir string

func (csd cdiSpecDir) UpdateConfigFile(in string) string {
	return fmt.Sprintf(`%s

[cdi]
  specDirs = [%q]
`, in, csd)
}

func runBuildkitd(
	conf *integration.BackendConfig,
	args []string,
	logs map[string]*bytes.Buffer,
	uid, gid int,
	extraEnv []string,
) (_, _ string, cl func() error, err error) {
	deferF := &integration.MultiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	tmpdir, err := os.MkdirTemp("", "bktest_buildkitd")
	if err != nil {
		return "", "", nil, err
	}

	if err := chown(tmpdir, uid, gid); err != nil {
		return "", "", nil, err
	}

	if err := os.MkdirAll(filepath.Join(tmpdir, "tmp"), 0711); err != nil {
		return "", "", nil, err
	}

	if err := chown(filepath.Join(tmpdir, "tmp"), uid, gid); err != nil {
		return "", "", nil, err
	}
	deferF.Append(func() error { return os.RemoveAll(tmpdir) })

	cfgfile, err := integration.WriteConfig(
		append(conf.DaemonConfig,
			withOTELSocketPath(getTraceSocketPath(tmpdir)),
			withCDISpecDir(conf.CDISpecDir),
		),
	)
	if err != nil {
		return "", "", nil, err
	}
	deferF.Append(func() error {
		return os.RemoveAll(filepath.Dir(cfgfile))
	})

	args = append(args, "--config="+cfgfile)
	address := getBuildkitdAddr(tmpdir)
	debugAddress := getBuildkitdDebugAddr(tmpdir)

	args = append(args, "--root", tmpdir, "--addr", address, "--debug")
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test utility
	cmd.Env = append(
		os.Environ(),
		"BUILDKIT_DEBUG_EXEC_OUTPUT=1",
		"BUILDKIT_DEBUG_PANIC_ON_ERROR=1",
		"BUILDKITD_DEBUGADDR="+debugAddress,
		"TMPDIR="+filepath.Join(tmpdir, "tmp"))
	if v := os.Getenv("GO_TEST_COVERPROFILE"); v != "" {
		coverDir := filepath.Join(filepath.Dir(v), "helpers")
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.SysProcAttr = getSysProcAttr()

	stop, err := integration.StartCmd(cmd, logs)
	if err != nil {
		return "", "", nil, err
	}
	deferF.Append(stop)

	if err := integration.WaitSocket(address, 15*time.Second, cmd); err != nil {
		return "", "", nil, err
	}

	// separated out since it's not required in windows
	deferF.Append(func() error {
		return mountInfo(tmpdir)
	})

	return address, debugAddress, cl, err
}
