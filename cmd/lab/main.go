package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/henanpengyuyan4444-beep/order-reliability-lab/internal/lab"
)

func main() {
	if err := run(); err != nil {
		slog.Error("lab_stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	data := flag.String("data", "data", "directory for synthetic local databases")
	port := flag.Int("port", 8080, "loopback HTTP port")
	workers := flag.Int("workers", 4, "worker count; 0 pauses processing")
	flag.Parse()
	if *port < 1 || *port > 65535 || *workers < 0 || *workers > 32 {
		return errors.New("invalid port or worker count")
	}
	if err := os.MkdirAll(*data, 0700); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s, err := lab.Open(filepath.Join(*data, "orders.db"), lab.DefaultConfig(), logger)
	if err != nil {
		return err
	}
	defer s.Close()
	m, err := lab.OpenMock(filepath.Join(*data, "provider.db"))
	if err != nil {
		return err
	}
	defer m.Close()
	providerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	provider := &http.Server{Handler: m.Handler(), ReadHeaderTimeout: 2 * time.Second}
	go provider.Serve(providerListener)
	defer provider.Close()
	client, err := lab.NewClient("http://"+providerListener.Addr().String(), 150*time.Millisecond)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		if *workers > 0 {
			_ = (&lab.Worker{Store: s, Gateway: client}).Run(ctx, *workers)
		}
	}()
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: lab.Handler(s, m), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	logger.Info("lab_started", "url", "http://"+server.Addr, "workers", *workers, "mode", "local-learning-only")
	select {
	case err = <-errorsCh:
	case <-ctx.Done():
	}
	stop()
	<-workerDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	_ = provider.Shutdown(shutdownCtx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
