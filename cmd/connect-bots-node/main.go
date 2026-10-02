// Connect Bots Node runs one private workspace on its own machine and connects
// outbound to an authenticated hub. It exposes no public HTTP listener.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	_ "github.com/chenhg5/cc-connect/agent/codex"
	_ "github.com/chenhg5/cc-connect/agent/pi"
	"github.com/chenhg5/cc-connect/bots"
	_ "github.com/chenhg5/cc-connect/platform/telegram"
)

var version = "dev"

type codexProcess interface {
	URL() string
	Done() <-chan struct{}
	Err() error
	Close() error
}

type nodeDependencies struct {
	pair       func(context.Context, string, string, bots.NodeMetadata, bool) (bots.NodeCredential, error)
	startCodex func(context.Context, bots.CodexAppServerConfig) (codexProcess, error)
	runClient  func(context.Context, bots.NodeClientConfig, http.Handler) error
	newRuntime func(*bots.Store, bots.RuntimeConfig) *bots.Runtime
	metadata   func() bots.NodeMetadata
	defaultDir func() (string, error)
}

func main() {
	configureLogging()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, defaultDependencies()); err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, bots.ErrNodeRevoked) {
			slog.Warn("Node access was revoked. Pair this computer again from Connect Bots Settings.")
			return
		}
		slog.Error("Connect Bots Node stopped", "error", err)
		os.Exit(1)
	}
}

func defaultDependencies() nodeDependencies {
	return nodeDependencies{
		pair: bots.PairNode,
		startCodex: func(ctx context.Context, cfg bots.CodexAppServerConfig) (codexProcess, error) {
			return bots.StartCodexAppServer(ctx, cfg)
		},
		runClient: bots.RunNodeClient,
		newRuntime: func(store *bots.Store, cfg bots.RuntimeConfig) *bots.Runtime {
			return bots.NewRuntime(store, cfg)
		},
		metadata: func() bots.NodeMetadata {
			hostname, _ := os.Hostname()
			return bots.NodeMetadata{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: version}
		},
		defaultDir: defaultDataDir,
	}
}

func execute(ctx context.Context, args []string, input io.Reader, output, errOutput io.Writer, deps nodeDependencies) error {
	if len(args) == 0 {
		return printHelp(output)
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return errors.New("unexpected positional arguments; use <command> --help")
		}
		return printHelp(output)
	case "version", "--version", "-version":
		if len(args) != 1 {
			return errors.New("unexpected positional arguments; use --help")
		}
		_, err := fmt.Fprintln(output, "Connect Bots Node "+version)
		return err
	case "pair":
		return pairCommand(ctx, args[1:], input, output, errOutput, deps)
	case "run":
		return runCommand(ctx, args[1:], output, errOutput, deps)
	case "status":
		return statusCommand(args[1:], output, errOutput, deps)
	default:
		return errors.New("unknown command; use connect-bots-node help")
	}
}

func printHelp(output io.Writer) error {
	_, err := io.WriteString(output, `Connect Bots Node

Usage:
  connect-bots-node pair --server https://bots.example.com
  connect-bots-node run
  connect-bots-node status
  connect-bots-node version

Create a host in Connect Bots Settings, then paste its pairing code when asked.
Bots, files, Codex authentication, and native skills stay on this machine.
The node connects outbound to the hub and does not open a public port.

Use <command> --help to see its options.
`)
	return err
}

func commandFlags(name string, errOutput io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	// Parse errors can echo arbitrary values. Keep them out of the log sink;
	// Usage is printed explicitly when --help was requested.
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		flags.SetOutput(errOutput)
		fmt.Fprintf(errOutput, "Usage: connect-bots-node %s [options]\n", name)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return flag.ErrHelp
		}
		return errors.New("invalid command options; use --help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use --help")
	}
	return nil
}

func pairCommand(ctx context.Context, args []string, input io.Reader, output, errOutput io.Writer, deps nodeDependencies) error {
	defaultDir, err := deps.defaultDir()
	if err != nil {
		return err
	}
	flags := commandFlags("pair", errOutput)
	data := flags.String("data", defaultDir, "private node workspace directory")
	var server string
	flags.StringVar(&server, "server", "", "Connect Bots hub URL")
	flags.StringVar(&server, "hub", "", "alias for --server")
	code := flags.String("code", "", "pairing code for automation; stdin is preferred")
	insecure := flags.Bool("allow-insecure", false, "explicitly allow HTTP to a non-loopback hub")
	replace := flags.Bool("replace", false, "replace pairing; existing bots/files/history become visible to the newly paired account")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if server = strings.TrimSpace(server); server == "" {
		return errors.New("pair requires --server")
	}
	root, err := privateDataDir(*data)
	if err != nil {
		return err
	}
	lock, err := acquireNodeLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := pairingDestination(root, *replace); err != nil {
		return err
	}
	if *replace {
		if _, err := fmt.Fprintln(errOutput, "Replacing pairing keeps this workspace's existing bots, files, and history. They become visible to the account that created the new pairing code."); err != nil {
			return err
		}
	}
	if *code == "" {
		if _, err := fmt.Fprint(errOutput, "Pairing code: "); err != nil {
			return err
		}
		*code, err = readPairingCode(ctx, input)
		if err != nil {
			return err
		}
	}
	*code = strings.TrimSpace(*code)
	if len(*code) == 0 || len(*code) > 512 || strings.ContainsAny(*code, "\r\n\x00") {
		return errors.New("a valid pairing code is required")
	}
	credential, err := deps.pair(ctx, server, *code, deps.metadata(), *insecure)
	if err != nil {
		// A pairing code is short-lived but still grants ownership of this
		// machine. Do not repeat it if a transport error included request data.
		return redactedError("pair node", err, *code)
	}
	config := nodeConfig{Version: nodeConfigVersion, Credential: credential, AllowInsecure: *insecure}
	if err := saveNodeConfig(root, config); err != nil {
		return redactedError("save node pairing", err, *code, credential.Token)
	}
	_, err = fmt.Fprintln(output, "Node paired. Run connect-bots-node run to start your bots.")
	return err
}

func readPairingCode(ctx context.Context, input io.Reader) (string, error) {
	// Closing stdin interrupts a blocked terminal/pipe read after Ctrl+C. This
	// command has no subsequent stdin consumer and exits after cancellation.
	stopCancellation := context.AfterFunc(ctx, func() {
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stopCancellation()
	reader := bufio.NewReader(io.LimitReader(input, 514))
	line, err := reader.ReadString('\n')
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("could not read the pairing code")
	}
	if len(line) > 513 {
		return "", errors.New("pairing code is too long")
	}
	return strings.TrimSpace(line), nil
}

func statusCommand(args []string, output, errOutput io.Writer, deps nodeDependencies) error {
	defaultDir, err := deps.defaultDir()
	if err != nil {
		return err
	}
	flags := commandFlags("status", errOutput)
	data := flags.String("data", defaultDir, "private node workspace directory")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	config, root, err := loadNodeConfig(*data)
	if errors.Is(err, os.ErrNotExist) {
		_, writeErr := fmt.Fprintln(output, "Not paired. Create a host in Connect Bots Settings, then run connect-bots-node pair.")
		return writeErr
	}
	if err != nil {
		return err
	}
	// This command reports saved pairing, not online state; the hub is the
	// authoritative source for connection status. Credentials are never shown.
	_, err = fmt.Fprintf(output, "Paired\nHub: %s\nNode: %s\nWorkspace: %s\nConnection status: see Hosts in Connect Bots Settings.\n", config.Credential.ServerURL, config.Credential.NodeID, root)
	return err
}

func runCommand(ctx context.Context, args []string, output, errOutput io.Writer, deps nodeDependencies) error {
	defaultDir, err := deps.defaultDir()
	if err != nil {
		return err
	}
	flags := commandFlags("run", errOutput)
	data := flags.String("data", defaultDir, "private node workspace directory")
	codexCommand := flags.String("codex-command", "codex", "installed Codex executable (absolute paths supported)")
	codexHome := flags.String("codex-home", "", "native Codex home; defaults to CODEX_HOME or ~/.codex")
	flov := flags.String("flov-url", "", "optional Flov transcription endpoint on this machine")
	insecure := flags.Bool("allow-insecure", false, "explicitly allow HTTP to a non-loopback hub (defaults to saved pairing choice)")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	config, root, err := loadNodeConfig(*data)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("node is not paired; run connect-bots-node pair first")
	}
	if err != nil {
		return err
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "allow-insecure" {
			config.AllowInsecure = *insecure
		}
	})
	options := nodeRunOptions{Root: root, CodexCommand: strings.TrimSpace(*codexCommand), CodexHome: strings.TrimSpace(*codexHome), FlovURL: strings.TrimSpace(*flov)}
	if options.CodexCommand == "" {
		return errors.New("codex-command must name an installed executable")
	}
	if _, err := fmt.Fprintln(output, "Starting Connect Bots Node. Press Ctrl+C to stop."); err != nil {
		return err
	}
	return redactedError("run node", runNode(ctx, config, options, deps), config.Credential.Token)
}

func redactedError(prefix string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return &commandError{message: prefix + ": " + message, cause: err}
}

type commandError struct {
	message string
	cause   error
}

func (err *commandError) Error() string { return err.message }
func (err *commandError) Unwrap() error { return err.cause }
