// Command telemetry records experiment provenance, host load, and Prometheus range
// queries under one run prefix.
//
// Example:
//
//	go run ./cmd/telemetry \
//	  --run-id exp1-n500-r1 --experiment exp1 --arm default \
//	  --out-prefix experiments/exp1-scale-sweep/N500/exp1-n500-r1/run \
//	  --duration 150s --prom-url http://127.0.0.1:9090 --prom-step 5s \
//	  --meta nodes=500 --meta scheduler=default-scheduler --meta qps=50 --meta seed=42
//
// Outputs: run-meta.json, run-host.csv, run-prometheus.csv, run-apiserver.csv,
// and run-pressure.csv.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/qiujiaro/gpu-fleet-scale-lab/pkg/telemetry"
)

type repeatedFlags []string

func (f *repeatedFlags) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type options struct {
	runID        string
	experiment   string
	arm          string
	outPrefix    string
	duration     time.Duration
	hostInterval time.Duration
	promURL      string
	promStep     time.Duration
	repoDir      string
	meta         repeatedFlags
	promQueries  repeatedFlags
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.runID, "run-id", "", "stable run identifier (required)")
	flag.StringVar(&opts.experiment, "experiment", "", "experiment ID, e.g. exp1 (required)")
	flag.StringVar(&opts.arm, "arm", "", "comparison arm, e.g. default or optimized")
	flag.StringVar(&opts.outPrefix, "out-prefix", "", "output prefix, e.g. results/run (required)")
	flag.DurationVar(&opts.duration, "duration", 0, "collection duration; 0 waits for SIGINT/SIGTERM")
	flag.DurationVar(&opts.hostInterval, "host-interval", time.Second, "host sampling interval; 0 disables")
	flag.StringVar(&opts.promURL, "prom-url", "", "Prometheus base URL; empty disables Prometheus export")
	flag.DurationVar(&opts.promStep, "prom-step", 5*time.Second, "Prometheus query_range step")
	flag.StringVar(&opts.repoDir, "repo-dir", ".", "repository directory used for Git provenance")
	flag.Var(&opts.meta, "meta", "run control key=value; repeatable; JSON scalars preserve type")
	flag.Var(&opts.promQueries, "prom-query", "additional name=PromQL range query; repeatable")
	flag.Parse()
	return opts
}

func main() {
	if err := run(parseFlags()); err != nil {
		log.Printf("telemetry: %v", err)
		os.Exit(1)
	}
}

func run(opts options) (retErr error) {
	if opts.runID == "" || opts.experiment == "" || opts.outPrefix == "" {
		return errors.New("--run-id, --experiment, and --out-prefix are required")
	}
	if opts.duration < 0 {
		return errors.New("--duration must not be negative")
	}
	if opts.hostInterval < 0 {
		return errors.New("--host-interval must not be negative")
	}
	if opts.promURL != "" && opts.promStep <= 0 {
		return errors.New("--prom-step must be positive when Prometheus is enabled")
	}

	controls := make(map[string]any, len(opts.meta))
	for _, assignment := range opts.meta {
		key, value, err := telemetry.ParseAssignment(assignment)
		if err != nil {
			return fmt.Errorf("parse --meta: %w", err)
		}
		if _, exists := controls[key]; exists {
			return fmt.Errorf("duplicate --meta key %q", key)
		}
		controls[key] = value
	}

	startedAt := time.Now().UTC()
	meta, err := telemetry.NewMetadata(opts.runID, opts.experiment, opts.arm, startedAt, controls)
	if err != nil {
		return err
	}
	meta["host_interval_seconds"] = opts.hostInterval.Seconds()
	if opts.hostInterval > 0 {
		meta["host_sampler"] = "ps process CPU/RSS sum"
	}
	if opts.promURL != "" {
		meta["prometheus_url"] = opts.promURL
		meta["prometheus_step_seconds"] = opts.promStep.Seconds()
	}
	gitCtx, gitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	sha, dirty, gitErr := telemetry.GitInfo(gitCtx, opts.repoDir)
	gitCancel()
	if gitErr != nil {
		meta["git_error"] = gitErr.Error()
	} else {
		meta["git_sha"] = sha
		meta["git_dirty"] = dirty
	}

	paths := outputPaths(opts.outPrefix)
	for _, path := range paths.required(opts.hostInterval > 0, opts.promURL != "") {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing artifact %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect output %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(opts.outPrefix), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := telemetry.WriteMetadata(paths.meta, meta); err != nil {
		return err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		telemetry.FinishMetadata(meta, time.Now().UTC(), "failed")
		if err := telemetry.WriteMetadata(paths.meta, meta); err != nil && retErr == nil {
			retErr = err
		}
	}()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	collectCtx := signalCtx
	var cancelDuration context.CancelFunc
	if opts.duration > 0 {
		collectCtx, cancelDuration = context.WithTimeout(signalCtx, opts.duration)
		defer cancelDuration()
	}

	hostErrCh := make(chan error, 1)
	if opts.hostInterval > 0 {
		hostFile, err := os.OpenFile(paths.host, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("create host CSV: %w", err)
		}
		go func() {
			err := telemetry.CollectHost(collectCtx, telemetry.PSHostSource{}, opts.hostInterval, hostFile)
			if closeErr := hostFile.Close(); err == nil {
				err = closeErr
			}
			hostErrCh <- err
		}()
	} else {
		hostErrCh <- nil
	}

	<-collectCtx.Done()
	endedAt := time.Now().UTC()
	if err := <-hostErrCh; err != nil {
		return fmt.Errorf("collect host telemetry: %w", err)
	}

	if opts.promURL != "" {
		queries, err := buildPromQueries(opts.promQueries)
		if err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var samples []telemetry.PromSample
		var missingQueries []string
		client := telemetry.PromClient{}
		for _, query := range queries {
			result, err := client.RangeQuery(queryCtx, opts.promURL, query, startedAt, endedAt, opts.promStep)
			if err != nil {
				return fmt.Errorf("range query %q: %w", query.Name, err)
			}
			if len(result) == 0 {
				log.Printf("telemetry: range query %q returned no samples; recording it as unavailable", query.Name)
				missingQueries = append(missingQueries, query.Name)
				continue
			}
			samples = append(samples, result...)
		}
		if err := writeExclusive(paths.prometheus, func(file *os.File) error {
			return telemetry.WritePrometheusCSV(file, samples)
		}); err != nil {
			return err
		}
		if err := writeExclusive(paths.apiserver, func(file *os.File) error {
			return telemetry.WriteAPIServerCSV(file, samples)
		}); err != nil {
			return err
		}
		if err := writeExclusive(paths.pressure, func(file *os.File) error {
			return telemetry.WritePressureCSV(file, samples)
		}); err != nil {
			return err
		}
		meta["prometheus_queries"] = queryNames(queries)
		if len(missingQueries) > 0 {
			meta["prometheus_missing_queries"] = missingQueries
		}
	}

	telemetry.FinishMetadata(meta, endedAt, "complete")
	if err := telemetry.WriteMetadata(paths.meta, meta); err != nil {
		return err
	}
	finished = true
	log.Printf("telemetry: wrote artifacts with prefix %s", opts.outPrefix)
	return nil
}

type artifactPaths struct {
	meta       string
	host       string
	prometheus string
	apiserver  string
	pressure   string
}

func outputPaths(prefix string) artifactPaths {
	return artifactPaths{
		meta:       prefix + "-meta.json",
		host:       prefix + "-host.csv",
		prometheus: prefix + "-prometheus.csv",
		apiserver:  prefix + "-apiserver.csv",
		pressure:   prefix + "-pressure.csv",
	}
}

func (p artifactPaths) required(host, prom bool) []string {
	paths := []string{p.meta}
	if host {
		paths = append(paths, p.host)
	}
	if prom {
		paths = append(paths, p.prometheus, p.apiserver, p.pressure)
	}
	return paths
}

func buildPromQueries(additional []string) ([]telemetry.PromQuery, error) {
	queries := append([]telemetry.PromQuery(nil), telemetry.DefaultPromQueries...)
	seen := make(map[string]struct{}, len(queries)+len(additional))
	for _, query := range queries {
		seen[query.Name] = struct{}{}
	}
	for _, assignment := range additional {
		name, expr, ok := strings.Cut(assignment, "=")
		name = strings.TrimSpace(name)
		expr = strings.TrimSpace(expr)
		if !ok || name == "" || expr == "" {
			return nil, fmt.Errorf("expected --prom-query name=PromQL, got %q", assignment)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate Prometheus query name %q", name)
		}
		seen[name] = struct{}{}
		queries = append(queries, telemetry.PromQuery{Name: name, Expr: expr})
	}
	return queries, nil
}

func queryNames(queries []telemetry.PromQuery) []string {
	names := make([]string, len(queries))
	for i, query := range queries {
		names[i] = query.Name
	}
	return names
}

func writeExclusive(path string, write func(*os.File) error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := write(file); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
