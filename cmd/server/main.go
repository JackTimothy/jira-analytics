// Command server runs the delivery-analytics API and serves the web client.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/adapter/configstore"
	"github.com/jacktimothy/jira-analytics/internal/adapter/github"
	"github.com/jacktimothy/jira-analytics/internal/adapter/httpapi"
	"github.com/jacktimothy/jira-analytics/internal/adapter/jira"
	"github.com/jacktimothy/jira-analytics/internal/adapter/tracelog"
	"github.com/jacktimothy/jira-analytics/internal/infra/config"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
	"github.com/jacktimothy/jira-analytics/internal/usecase"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("startup failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// run is the composition root: the only place that knows which concrete
// adapters exist. Everything it builds is wired together through the ports, so
// no inner package names Jira or GitHub.
func run(logger *slog.Logger) error {
	settings, err := config.Load()
	if err != nil {
		return err
	}

	projects, err := configstore.Load(settings.ProjectsFile)
	if err != nil {
		return err
	}

	client := httpclient.New(&http.Client{
		Timeout:   30 * time.Second,
		Transport: apiTransport(),
	})

	tracker := jira.NewTracker(jira.Config{
		BaseURL:  settings.JiraBaseURL,
		Email:    settings.JiraEmail,
		APIToken: settings.JiraAPIToken,
	}, client, jira.WithLogger(logger))

	codeHost := github.NewCodeHost(github.Config{
		BaseURL: settings.GitHubBaseURL,
		Token:   settings.GitHubToken,
	}, client)

	api := httpapi.NewServer(
		projects,
		usecase.NewSprints(projects, tracker),
		usecase.NewRetrospective(projects, tracker, codeHost,
			usecase.WithTracer(tracelog.New(logger, tracelog.WithRequestCounter(client.Requests)))),
		logger,
	)

	server := &http.Server{
		Addr:              ":" + settings.Port,
		Handler:           httpapi.WithStaticFiles(api.Routes(), settings.WebDir, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return serve(server, logger)
}

// apiTransport is deliberately not http.DefaultTransport.
//
// The default caps idle connections at two per host. Assembling one
// retrospective fires hundreds of concurrent requests at two hosts, so with the
// default all but two of them would find no idle connection waiting and pay a
// fresh TCP and TLS handshake — which costs more than the round trip itself and
// quietly serialises work that was carefully made parallel.
func apiTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	return transport
}

// serve runs the server until interrupted, then drains in-flight requests.
// A retrospective takes a while to assemble, so cutting requests off at the
// moment of shutdown would waste work already paid for in API quota.
func serve(server *http.Server, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
