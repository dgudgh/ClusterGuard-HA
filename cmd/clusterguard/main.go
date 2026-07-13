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

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to ClusterGuard HA JSON configuration")
	flag.Parse()
	configuration, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("configuration error: %v", err)
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
	httpServer := &http.Server{Addr: configuration.HTTPAddress, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		if err := httpServer.Close(); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()
	fmt.Printf("ClusterGuard HA listening on http://%s/\n", configuration.HTTPAddress)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
