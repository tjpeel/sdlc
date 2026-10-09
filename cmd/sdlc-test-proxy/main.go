package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tjpeel/sdlc/internal/testproxy"
)

func main() {
	var config testproxy.Config
	flags := flag.NewFlagSet("sdlc-test-proxy", flag.ExitOnError)
	flags.StringVar(&config.Session, "session", "", "controller session")
	flags.StringVar(&config.ProxyID, "proxy-id", "", "trusted namespace container ID")
	flags.StringVar(&config.Socket, "daemon-socket", "/sdlc/daemon/docker.sock", "controller daemon socket")
	flags.StringVar(&config.Workspace, "workspace", "", "daemon workspace")
	flags.StringVar(&config.SocketSource, "socket-source", "", "session socket in daemon")
	flags.Parse(os.Args[1:])
	if config.ProxyID == "" {
		config.ProxyID, _ = os.Hostname()
	}
	proxy, err := testproxy.New(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer proxy.Close()
	// The controller precreates this owned directory. Never replace an existing
	// socket: that could disconnect a live session after an accidental restart.
	listener, err := net.Listen("unix", "/run/sdlc/docker.sock")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open owned session socket")
		os.Exit(1)
	}
	defer listener.Close()
	if err := os.Chmod("/run/sdlc/docker.sock", 0660); err != nil {
		fmt.Fprintln(os.Stderr, "cannot set session socket permissions")
		os.Exit(1)
	}
	proxy.WatchPorts()
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 * 1024}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Println("SDLC scoped test proxy ready")
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "session proxy stopped unexpectedly")
		os.Exit(1)
	}
}
