package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/runtime"
)

const defaultConfigPath = "/etc/clusterguard/clusterguard.json"

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

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to ClusterGuard HA JSON configuration")
	flag.Parse()
	configuration, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	logConfigurationWarnings(log.Default(), configuration.DeprecationWarnings)
	server, err := runtime.New(configuration)
	if err != nil {
		log.Fatalf("startup error: %v", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			log.Printf("control plane shutdown error: %v", err)
		}
	}()
	httpServer := &http.Server{Addr: configuration.HTTPAddress, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		if err := httpServer.Close(); err != nil {
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
