package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/internal/buildinfo"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/runtime"
)

const defaultConfigPath = "/etc/clusterguard/clusterguard.json"

const gracefulShutdownTimeout = 30 * time.Second

type serverOptions struct {
	configPath  string
	checkConfig bool
}

func parseServerFlags(arguments []string, stderr io.Writer) (serverOptions, error) {
	flags := flag.NewFlagSet("clusterguard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path to ClusterGuard HA JSON configuration")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	if err := flags.Parse(arguments); err != nil {
		return serverOptions{}, err
	}
	if flags.NArg() != 0 {
		return serverOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return serverOptions{configPath: *configPath, checkConfig: *checkConfig}, nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func shutdownHTTPServer(server *http.Server, timeout time.Duration) error {
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	return nil
}

func logConfigurationWarnings(logger *log.Logger, warnings []string) {
	if logger == nil {
		return
	}
	for _, warning := range warnings {
		if warning != "" {
			logger.Printf("configuration warning: %s", warning)
		}
	}
}

func runAdmin(arguments []string, stdout, stderr io.Writer, random io.Reader, now func() time.Time) int {
	if len(arguments) == 0 || arguments[0] != "prepare-recovery" {
		_, _ = fmt.Fprintln(stderr, "clusterguard: admin requires the prepare-recovery subcommand")
		return 2
	}
	flags := flag.NewFlagSet("clusterguard admin prepare-recovery", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", platformauth.DefaultAdminRecoveryFile, "one-time recovery artifact path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "clusterguard: prepare-recovery does not accept positional arguments")
		return 2
	}
	password, err := platformauth.GenerateTemporaryPassword(random)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "clusterguard: generate temporary password failed")
		return 1
	}
	artifact, err := platformauth.NewAdminRecoveryArtifact(
		password, platformauth.DefaultArgon2Hasher(random), now,
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "clusterguard: prepare recovery artifact failed")
		return 1
	}
	if err := platformauth.WriteAdminRecoveryArtifact(*output, artifact); err != nil {
		_, _ = fmt.Fprintln(stderr, "clusterguard:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Administrator recovery artifact written to %s\n", *output)
	_, _ = fmt.Fprintf(stdout, "Temporary password (shown once): %s\n", password)
	return 0
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		info := buildinfo.Current("clusterguard")
		fmt.Printf("%s %s-%s (%s, state-format=%d, update-protocol=%d)\n",
			info.Product, info.Version, info.Release, info.Commit, info.StateFormat, info.UpdateProtocol)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version-json" {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current("clusterguard"))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdmin(os.Args[2:], os.Stdout, os.Stderr, rand.Reader, time.Now))
	}
	options, err := parseServerFlags(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatalf("argument error: %v", err)
	}
	configuration, err := config.Load(options.configPath)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	logConfigurationWarnings(log.Default(), configuration.DeprecationWarnings)
	if options.checkConfig {
		if err := runtime.ValidateConfiguration(configuration); err != nil {
			log.Fatalf("configuration preflight error: %v", err)
		}
		fmt.Printf("ClusterGuard HA configuration is valid: %s\n", options.configPath)
		return
	}
	server, err := runtime.New(configuration)
	if err != nil {
		log.Fatalf("startup error: %v", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			log.Printf("control plane shutdown error: %v", err)
		}
	}()
	httpServer := newHTTPServer(configuration.HTTPAddress, server.Handler())
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		if err := shutdownHTTPServer(httpServer, gracefulShutdownTimeout); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()
	scheme := "http"
	serve := httpServer.ListenAndServe
	if configuration.TLSCertFile != "" {
		scheme = "https"
		serve = func() error { return httpServer.ListenAndServeTLS(configuration.TLSCertFile, configuration.TLSKeyFile) }
	}
	fmt.Printf("ClusterGuard HA listening on %s://%s/\n", scheme, configuration.HTTPAddress)
	if err := serve(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
