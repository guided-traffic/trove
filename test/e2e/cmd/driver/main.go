// Command driver is the end-to-end test driver of the dev cluster (E0.6, E0.13,
// E0.14). M0 has no API that writes a change or enqueues a job, so the driver
// does what a web replica would do, with the packages a web replica uses: it
// connects to PostgreSQL and Valkey with the release's own configuration and
// credentials (the pod mounts the same ConfigMap and Secrets), writes changes
// through internal/cluster and enqueues jobs through internal/jobs. The workers
// of the release, built from the release image, run the jobs.
//
// The driver is not part of the release image. test/e2e/Dockerfile wraps it
// into its own image, and deploy/dev/e2e.sh runs it as a pod next to the release.
//
//	driver idle                              wait for exec commands until SIGTERM
//	driver info                              print the bounds of P-13 and P-14 and of R-HA-057's outage as JSON
//	driver load [-duration 60s] [-every 1s]  write a change and enqueue a job each tick, then print the summary
//	driver probe [-steps 90] [-interval-ms 1000]   enqueue one long system.probe job and print its id
//	driver stop                              end a running load after its current tick
//	driver job <id>                          print the job row as JSON
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// kindProbe is the diagnostic job kind the release's workers run (internal/jobs/probe.go): it counts
// steps, persists its cursor after each one and records which workers carried it. The driver only
// enqueues it, so it declares the kind and its args on its own side of the wire.
// stopFile ends a running `driver load` after its current tick: `driver stop` creates it.
const stopFile = "/tmp/e2e-stop"

const kindProbe = "system.probe"

type probeArgs struct {
	Steps      int `json:"steps"`
	IntervalMS int `json:"interval_ms"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "driver: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: driver idle|info|load|probe|job")
	}
	cmd, args := args[0], args[1:]
	if cmd == "idle" {
		<-ctx.Done()
		return nil
	}
	loaded, err := config.Load(config.Options{File: "/etc/trove/trove.yaml", Environ: os.Environ(), Command: config.CommandMigrate})
	if err != nil {
		return err
	}
	cfg := loaded.Config
	switch cmd {
	case "info":
		return printJSON(out, map[string]any{
			"epoch_poll_interval_ms": cfg.Cluster.EpochPollInterval.Milliseconds(),
			"jobs_poll_interval_ms":  cfg.Jobs.PollInterval.Milliseconds(),
			"retry_max_elapsed_ms":   cfg.Database.Retry.MaxElapsed.Milliseconds(),
			// R-HA-057's bound for degraded to clear after a Valkey outage
			"valkey_open_duration_ms":         cfg.Valkey.CircuitBreaker.OpenDuration.Milliseconds(),
			"valkey_health_check_interval_ms": cfg.Valkey.HealthCheckInterval.Milliseconds(),
		})
	case "stop":
		return os.WriteFile(stopFile, nil, 0o600)
	case "load":
		return loadCmd(ctx, cfg, args, out)
	case "probe":
		return probeCmd(ctx, cfg, args, out)
	case "job":
		return jobCmd(ctx, cfg, args, out)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// peer is one connection set of the driver, built like a web replica builds its own.
type peer struct {
	name    string
	db      *postgres.DB
	vk      *cluster.Valkey
	signals *cluster.Signals
	changes *cluster.Changes
	queue   *jobs.Queue
	reg     *prometheus.Registry
}

func (p *peer) close() {
	if p.changes != nil {
		_ = p.changes.Stop(context.Background())
	}
	if p.signals != nil {
		p.signals.Close()
	}
	if p.vk != nil {
		p.vk.Close()
	}
	if p.db != nil {
		p.db.Close()
	}
}

// openPeer connects with the role web, so the peer claims nothing and holds no lease.
func openPeer(ctx context.Context, cfg *config.Config, name string, listeners ...cluster.Listener) (*peer, error) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})).With("peer", name)
	p := &peer{name: name, reg: prometheus.NewRegistry()}
	var err error
	if p.db, err = postgres.Open(ctx, cfg.Database, name, "web", log); err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	facts, err := p.db.SystemFacts(ctx)
	if err != nil {
		p.close()
		return nil, fmt.Errorf("system_state: %w", err)
	}
	if p.vk, err = cluster.NewValkey(ctx, cfg.Valkey, name, "web", log, prometheus.NewRegistry(), cluster.WithKeyspaceID(strings.ReplaceAll(facts.KeyspaceID, "-", ""))); err != nil {
		p.close()
		return nil, fmt.Errorf("valkey: %w", err)
	}
	if p.signals, err = cluster.NewSignals(cluster.SignalsOptions{Bus: p.vk, Config: cfg.Cluster, Log: log, Metrics: p.reg}); err != nil {
		p.close()
		return nil, err
	}
	if p.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: p.db.ControlPool(), Bus: p.vk, Config: cfg.Cluster, WatchdogTimeout: cfg.Health.WatchdogTimeout, Log: log, Metrics: p.reg,
	}); err != nil {
		p.close()
		return nil, err
	}
	for _, l := range listeners {
		p.changes.AddListener(l)
	}
	if err := p.changes.Start(ctx); err != nil {
		p.close()
		return nil, fmt.Errorf("changes: %w", err)
	}
	// The registry holds the kinds so the queue accepts them; the handlers run on the workers.
	r := jobs.NewRegistry()
	r.RegisterKind(kindProbe, jobs.KindSpec{MaxAttempts: 3, Handler: func(context.Context, *jobs.Run) error { return nil }})
	r.Freeze()
	p.queue = jobs.NewQueue(jobs.QueueOptions{DB: p.db.Main(), Registry: r, Signals: p.signals, Log: log})
	return p, nil
}

func probeCmd(ctx context.Context, cfg *config.Config, args []string, out io.Writer) error {
	steps, interval := 90, 1000
	for i := 0; i+1 < len(args); i += 2 {
		var err error
		switch args[i] {
		case "-steps":
			_, err = fmt.Sscan(args[i+1], &steps)
		case "-interval-ms":
			_, err = fmt.Sscan(args[i+1], &interval)
		default:
			err = fmt.Errorf("unknown flag %q", args[i])
		}
		if err != nil {
			return err
		}
	}
	p, err := openPeer(ctx, cfg, instance("e2e-probe"))
	if err != nil {
		return err
	}
	defer p.close()
	res, err := p.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: kindProbe, Args: probeArgs{Steps: steps, IntervalMS: interval}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, res.Job.ID)
	return err
}

func jobCmd(ctx context.Context, cfg *config.Config, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: driver job <id>")
	}
	p, err := openPeer(ctx, cfg, instance("e2e-job"))
	if err != nil {
		return err
	}
	defer p.close()
	j, ok, err := p.queue.Get(ctx, args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("job %s does not exist", args[0])
	}
	return printJSON(out, map[string]any{
		"id": j.ID, "kind": j.Kind, "state": j.State, "attempt": j.Attempt, "max_attempts": j.MaxAttempts,
		"locked_by": j.LockedBy, "progress_done": j.ProgressDone, "progress_total": j.ProgressTotal,
		"checkpoint": json.RawMessage(orNull(j.Checkpoint)), "result": json.RawMessage(orNull(j.Result)), "error": json.RawMessage(orNull(j.Error)),
		"created_at": j.CreatedAt, "started_at": j.StartedAt, "finished_at": j.FinishedAt,
	})
}

func orNull(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

// instance names a peer after the pod and the purpose, so application_name in
// PostgreSQL and the client name in Valkey show whose connection it is.
func instance(purpose string) string {
	pod := os.Getenv("TROVE_POD_NAME")
	if pod == "" {
		pod, _ = os.Hostname()
	}
	return fmt.Sprintf("%s-%s-%d", purpose, pod, time.Now().UnixNano()%100000)
}
