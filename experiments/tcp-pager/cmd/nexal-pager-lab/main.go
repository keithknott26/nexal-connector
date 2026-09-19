// nexal-pager-lab is an isolated acceptance harness, not the production connector.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nexal/experiments/tcp-pager/lab"
	"nexal/experiments/tcp-pager/pager"
)

var errOSRAM = errors.New("OS-visible RAM requirement NOT IMPLEMENTED: paging is not host RAM, guest OS RAM or VRAM expansion")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Nexal pager lab:", err)
		if errors.Is(err, errOSRAM) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".nexal-pager-lab")
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support", "Nexal Pager Lab")
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0077 != 0 {
		return "", errors.New("lab storage must be a private directory, not a symlink")
	}
	return base, nil
}

// Reserve a private parent; the actual destination remains nonexistent so the
// credential/output writer can refuse overwrites even after interrupted runs.
func destination(prefix string) (string, error) {
	base, err := root()
	if err != nil {
		return "", err
	}
	parent, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, "run"), nil
}

func promptLine(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024), 4096)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", errors.New("interactive input unavailable; supply the explicit flag")
	}
	return strings.TrimSpace(sc.Text()), nil
}

func endpoint() (string, error) {
	ips, err := lab.Addresses()
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", errors.New("no private LAN address found; connect to the intended private network")
	}
	if len(ips) == 1 {
		return net.JoinHostPort(ips[0], "9443"), nil
	}
	fmt.Fprintln(os.Stderr, "Choose the M4/private donor interface reachable by the M2 (not a VPN):")
	for i, ip := range ips {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, ip)
	}
	answer, err := promptLine("Interface number: ")
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(ips) {
		return "", errors.New("invalid interface choice")
	}
	return net.JoinHostPort(ips[n-1], "9443"), nil
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("commands: donor | serve | receive | local-ram-test | addresses")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	state := f.String("state", "", "new donor directory; defaults to private per-run storage")
	listen := f.String("listen", "", "numeric private IP:port; prompts if multiple interfaces")
	bundle := f.String("bundle", "", "transferred client folder")
	output := f.String("output", "", "new receiver directory; defaults to private per-run storage")
	helper := f.String("native-helper", "", "explicit locally built HVF helper")
	fingerprint := f.String("ca-fingerprint", "", "public CA fingerprint displayed on donor; prompts if omitted")
	portable := f.Bool("portable-only", false, "transport test only, no native CPU-fault claim")
	loopback := f.Bool("loopback-test", false, "explicit development-only same-computer acceptance")
	requireOS := f.Bool("require-os-ram", false, "require unimplemented OS-visible RAM; exit 3 after successful paging")
	show := f.Bool("show-folder", false, "reveal client transfer folder in Finder on macOS")
	lifetime := f.Duration("lifetime", 30*time.Minute, "donor maximum lifetime, 1s..30m")
	sessions := f.Int("sessions", 16, "maximum authenticated disposable sessions, 1..16")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments; quote paths containing spaces")
	}
	if *lifetime < time.Second || *lifetime > 30*time.Minute || *sessions < 1 || *sessions > 16 {
		return errors.New("invalid lifetime or session limit")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "local-ram-test":
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || *helper == "" || *portable {
			return errors.New("local OS RAM observation requires Apple-silicon macOS and the native helper")
		}
		if *state != "" || *listen != "" || *bundle != "" {
			return errors.New("local-ram-test creates isolated loopback state; donor flags are not accepted")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer ln.Close()
		localState, err := destination("local-ram-donor-")
		if err != nil {
			return err
		}
		if _, err = lab.Prepare(localState, ln.Addr().String(), true); err != nil {
			return err
		}
		conf, err := pager.LoadTLS(filepath.Join(localState, "donor"), true)
		if err != nil {
			return err
		}
		if *output == "" {
			*output, err = destination("local-ram-result-")
			if err != nil {
				return err
			}
		}
		localCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- pager.ServeLab(localCtx, ln, conf, lab.Pages, 4) }()
		fmt.Fprintln(os.Stderr, "Observing actual macOS hw.memsize before, during and after native paging.")
		fmt.Fprintln(os.Stderr, "Loopback donor only. No system counters/settings are modified.")
		r, runErr := lab.Receive(localCtx, filepath.Join(localState, "client"), *output, *helper, false, true)
		cancel()
		serverErr := <-done
		fmt.Fprintf(os.Stderr, "Receiver evidence directory: %s\n", *output)
		if err = json.NewEncoder(os.Stdout).Encode(r); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if serverErr != nil {
			return serverErr
		}
		fmt.Fprintln(os.Stderr, "CPU paging passed; the OS-visible RAM requirement has not passed.")
		return errOSRAM
	case "addresses":
		ips, err := lab.Addresses()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(ips)
	case "donor", "serve":
		var err error
		if os.Args[1] == "donor" {
			if *listen == "" {
				*listen, err = endpoint()
				if err != nil {
					return err
				}
			}
			if *state == "" {
				*state, err = destination("donor-")
				if err != nil {
					return err
				}
			}
			if _, err = lab.Prepare(*state, *listen, *loopback); err != nil {
				return err
			}
		} else if *state == "" {
			return errors.New("serve requires the original --state directory")
		}
		m, err := lab.LoadManifest(filepath.Join(*state, "donor"), *loopback)
		if err != nil {
			return err
		}
		local, err := lab.IsLocal(m.Endpoint)
		if err != nil {
			return err
		}
		if !local {
			return errors.New("donor address is no longer assigned to this computer; prepare a new run")
		}
		config, err := pager.LoadTLS(filepath.Join(*state, "donor"), true)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", m.Endpoint)
		if err != nil {
			return fmt.Errorf("bind failed; do not kill other applications: %w", err)
		}
		defer ln.Close()
		fmt.Fprintf(os.Stderr, "\nPRIVATE DISPOSABLE PAGER LAB\nEndpoint: %s\nPayload per session: %d bytes\nMaximum lifetime: %s; authenticated sessions: %d\n",
			m.Endpoint, lab.Pages*pager.PageSize, *lifetime, *sessions)
		fmt.Fprintf(os.Stderr, "State directory: %s\nTransfer ONLY this client folder to your other Mac:\n%s\n",
			*state, filepath.Join(*state, "client"))
		fmt.Fprintf(os.Stderr, "Public CA fingerprint (compare on receiver):\n%s\n", m.CAFingerprint)
		fmt.Fprintln(os.Stderr, "Keep this terminal open. Ctrl+C stops the donor. No public-cloud resources or real application data.")
		if *show && runtime.GOOS == "darwin" {
			oc, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd := exec.CommandContext(oc, "/usr/bin/open", "-R", filepath.Join(*state, "client"))
			cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
			if cmd.Run() != nil {
				fmt.Fprintln(os.Stderr, "Finder did not open; use the client folder path above.")
			}
			cancel()
		}
		dctx, cancel := context.WithTimeout(ctx, *lifetime)
		defer cancel()
		err = pager.ServeLab(dctx, ln, config, lab.Pages, *sessions)
		fmt.Fprintln(os.Stderr, "Donor stopped. Each completed lab connection used a fresh disposable page store.")
		return err
	case "receive":
		if *bundle == "" {
			return errors.New("receive requires --bundle /path/to/transferred/client")
		}
		m, err := lab.LoadManifest(*bundle, *loopback)
		if err != nil {
			return err
		}
		if *fingerprint == "" {
			fmt.Fprintf(os.Stderr, "Bundle endpoint: %s\nConfirm the donor using its terminal, not this transferred folder.\n", m.Endpoint)
			*fingerprint, err = promptLine("Paste the PUBLIC CA fingerprint printed on the donor: ")
			if err != nil {
				return err
			}
		}
		if *fingerprint != m.CAFingerprint {
			return errors.New("donor fingerprint mismatch; no connection attempted")
		}
		if *output == "" {
			*output, err = destination("receiver-")
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(os.Stderr, "Importing only credentials/manifest into private storage. No bundle code will execute.")
		fmt.Fprintln(os.Stderr, "The transferred folder contains a private client key: keep it private and remove the transfer copy after testing.")
		r, err := lab.Receive(ctx, *bundle, *output, *helper, *portable, *loopback)
		fmt.Fprintf(os.Stderr, "Receiver evidence directory: %s\n", *output)
		if e := json.NewEncoder(os.Stdout).Encode(r); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Paging suite passed. Host/guest OS RAM and GPU expansion remain NOT IMPLEMENTED.")
		if *requireOS {
			return errOSRAM
		}
		return nil
	default:
		return errors.New("unknown command")
	}
}
