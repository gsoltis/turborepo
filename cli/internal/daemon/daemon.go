package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/fs"
	"github.com/vercel/turborepo/cli/internal/ui"
	"github.com/vercel/turborepo/cli/internal/util"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Command struct {
	Config *config.Config
	UI     cli.Ui
}

// Run runs the daemon command
func (c *Command) Run(args []string) int {
	cmd := getCmd(c.Config, c.UI)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		return 1
	}
	return 0
}

// Help returns information about the `daemon` command
func (c *Command) Help() string {
	cmd := getCmd(c.Config, c.UI)
	return util.HelpForCobraCmd(cmd)
}

// Synopsis of daemon command
func (c *Command) Synopsis() string {
	cmd := getCmd(c.Config, c.UI)
	return cmd.Short
}

type daemon struct {
	ui       cli.Ui
	logger   hclog.Logger
	fsys     afero.Fs
	repoRoot fs.AbsolutePath
}

func (d *daemon) getUnixSocket() fs.AbsolutePath {
	tempDir := fs.GetTempDir(d.fsys, "turbod")

	pathHash := sha256.Sum256([]byte(d.repoRoot.ToString()))
	// We grab a substring of the hash because there is a 108-character limit on the length
	// of a filepath for unix domain socket.
	hexHash := hex.EncodeToString(pathHash[:])[:16]
	return tempDir.Join(fmt.Sprintf("%v.sock", hexHash))
}

// logError logs an error and outputs it to the UI.
func (d *daemon) logError(err error) {
	d.logger.Error("error", err)
	d.ui.Error(fmt.Sprintf("%s%s", ui.ERROR_PREFIX, color.RedString(" %v", err)))
}

func getCmd(config *config.Config, ui cli.Ui) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "turbo daemon",
		Short:         "Runs turbod",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := &daemon{
				ui:       ui,
				logger:   config.Logger,
				fsys:     config.Fs,
				repoRoot: config.Cwd,
			}
			err := d.runTurboServer()
			if err != nil {
				d.logError(err)
			}
			return err
		},
	}
	return cmd
}

type turboServer struct {
	UnimplementedTurboServer
}

func (ts *turboServer) GetGlobalHash(ctx context.Context, req *GlobalHashRequest) (*GlobalHashReply, error) {
	hash := "foo"
	return &GlobalHashReply{Hash: []byte(hash)}, nil
}

func (d *daemon) debounceServers(sockPath fs.AbsolutePath) error {
	if !sockPath.FileExists() {
		return nil
	}
	// The socket file exists, can we connect to it?
}

func (d *daemon) runTurboServer() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sockPath := d.getUnixSocket()
	fmt.Printf("Using socket path %v (%v)", sockPath, len(sockPath))
	lis, err := net.Listen("unix", sockPath.ToString())
	if err != nil {
		return err
	}
	timeout := newTimeout(10*time.Second, ctx)
	go timeout.loop()
	s := grpc.NewServer(grpc.UnaryInterceptor(timeout.onRequest))
	server := &turboServer{}
	RegisterTurboServer(s, server)
	errCh := make(chan error)
	go func(errCh chan<- error) {
		if err := s.Serve(lis); err != nil {
			errCh <- err
		}
		close(errCh)
	}(errCh)
	select {
	case err, ok := <-errCh:
		{
			if ok {
				fmt.Printf("got err: %v", err)
			}
			cancel()
		}
	case <-timeout.timedOut:
		fmt.Printf("server timed out")
		s.Stop()
	}
	return nil
}

type daemonTimeout struct {
	timeout  time.Duration
	reqCh    chan struct{}
	timedOut chan struct{}
	ctx      context.Context
}

func newTimeout(timeout time.Duration, ctx context.Context) *daemonTimeout {
	return &daemonTimeout{
		timeout:  timeout,
		reqCh:    make(chan struct{}),
		timedOut: make(chan struct{}),
		ctx:      ctx,
	}
}

func (dt *daemonTimeout) onRequest(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	dt.reqCh <- struct{}{}
	return handler(ctx, req)
}

func (dt *daemonTimeout) loop() {
	timeoutCh := time.After(dt.timeout)
	for {
		select {
		case <-dt.reqCh:
			timeoutCh = time.After(dt.timeout)
		case <-timeoutCh:
			close(dt.timedOut)
			break
		case <-dt.ctx.Done():
			break
		}
	}

}

func RunClient() error {
	creds := insecure.NewCredentials()
	addr := "localhost:5555"
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c := NewTurboClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
	defer cancel()
	r, err := c.GetGlobalHash(ctx, &GlobalHashRequest{})
	if err != nil {
		return err
	}
	fmt.Printf("Got Hash: %v\n", string(r.Hash))
	return nil
}
