package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/zyno-io/v8s/agent/internal/version"
	"github.com/zyno-io/v8s/agent/internal/webui"
)

const usage = `usage: v8s <command> [flags]

commands:
  agent      run the agent (used by the desktop app and for development)
  serve      run the agent on loopback, serve the embedded UI, and open the browser
  server     run the self-hosted multi-user server (OIDC login, per-user engines): --config /etc/v8s/config.yaml
  version    print the version
`

func dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "agent":
		return runAgent(args[1:], agentHooks{})
	case "serve":
		return runServe(args[1:])
	case "server":
		return runServer(args[1:])
	case "version", "--version", "-v":
		fmt.Println("v8s", version.Version)
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return errUsage
}

var errUsage = errors.New("usage error")

// agentHooks lets subcommands customize the agent run without forking it.
type agentHooks struct {
	// onReady replaces the stdout `{port, token}` banner when set.
	onReady func(port int, token string)
}

// withEmbeddedUI serves the embedded UI at / for everything the agent does not handle itself.
func withEmbeddedUI(agent http.Handler) http.Handler {
	ui := webui.Handler()
	mux := http.NewServeMux()
	mux.Handle("/ws", agent)
	mux.Handle("/stream", agent)
	mux.Handle("/stream/", agent)
	mux.Handle("/healthz", agent)
	mux.Handle("/", ui)
	return mux
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 7931, "port to listen on (loopback only)")
	noBrowser := fs.Bool("no-browser", false, "print the URL without opening a browser")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: client-go loading rules, $KUBECONFIG merged)")
	noUpdateCheck := fs.Bool("no-update-check", false, "do not check v8s.io for a newer release (also: V8S_UPDATE_CHECK=off)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid --port %d", *port)
	}
	if !webui.Available() {
		return errors.New("this build has no embedded UI (build with scripts/build-agent.sh, or run `v8s agent --static-dir <ui dist>`)")
	}
	agentArgs := []string{"--listen", "127.0.0.1:" + strconv.Itoa(*port)}
	if *kubeconfig != "" {
		agentArgs = append(agentArgs, "--kubeconfig", *kubeconfig)
	}
	if *noUpdateCheck {
		agentArgs = append(agentArgs, "--no-update-check")
	}
	return runAgent(agentArgs, agentHooks{onReady: func(p int, token string) {
		url := fmt.Sprintf("http://localhost:%d/#agent=ws://localhost:%d/ws&token=%s", p, p, token)
		fmt.Println("v8s is running. Open:")
		fmt.Println(url)
		if !*noBrowser {
			if err := openBrowser(url); err != nil {
				fmt.Fprintln(os.Stderr, "could not open a browser:", err)
			}
		}
	}})
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
