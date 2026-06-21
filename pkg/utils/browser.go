package utils

import (
	"os"
	"os/exec"

	"github.com/cli/browser"
	"github.com/cli/safeexec"
	"github.com/google/shlex"
)

func OpenBrowser(url string) error {
	launcher := os.Getenv("BROWSER")
	if launcher != "" {
		launcherArgs, err := shlex.Split(launcher)
		if err != nil {
			return err
		}
		launcherExec, err := safeexec.LookPath(launcherArgs[0])
		if err != nil {
			return err
		}
		args := append(launcherArgs[1:], url)
		cmd := exec.Command(launcherExec, args...) //nolint:gosec
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		return cmd.Run()
	}
	return browser.OpenURL(url)
}
