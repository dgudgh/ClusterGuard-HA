package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"clusterguard.io/ha/internal/agent"
)

func main() {
	configurationPath := flag.String("config", "/etc/clusterguard/agent.json", "agent configuration path")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit")
	reconcile := flag.Bool("reconcile", false, "reconcile local VIP ownership against the majority controller")
	flag.Parse()
	configuration, err := agent.LoadConfig(*configurationPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runner := agent.OSCommandRunner{}
	vip := agent.NewLinuxVIPController(runner, configuration.IPBinary, configuration.ARPingBinary)
	roles := agent.NewMySQLRoleController(runner, configuration.MySQLBinary, configuration.RoleStateDirectory)
	if *checkConfig {
		if len(configuration.ControllerURLs) > 0 {
			if _, err := agent.NewControllerHTTPClient(configuration); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		return
	}
	if *reconcile {
		httpClient, err := agent.NewControllerHTTPClient(configuration)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		decisions, err := agent.NewHTTPReconcileClient(configuration.ControllerURLs, configuration.SharedSecret, httpClient, configuration.AllowInsecureHTTP, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		results, reconcileErr := agent.NewReconciler(vip, roles, decisions).ReconcileAll(context.Background(), configuration.Clusters)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"status": "completed", "results": results})
		if reconcileErr != nil {
			fmt.Fprintln(os.Stderr, reconcileErr)
			os.Exit(3)
		}
		return
	}
	service, err := agent.NewService(configuration, vip, roles, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	request := agent.Request{}
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, "invalid agent request")
		os.Exit(2)
	}
	response := service.Handle(context.Background(), request)
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		fmt.Fprintln(os.Stderr, "write agent response failed")
		os.Exit(1)
	}
}
