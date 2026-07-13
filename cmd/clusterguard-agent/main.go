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
	flag.Parse()
	configuration, err := agent.LoadConfig(*configurationPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *checkConfig {
		return
	}
	runner := agent.OSCommandRunner{}
	service, err := agent.NewService(
		configuration,
		agent.NewLinuxVIPController(runner, configuration.IPBinary, configuration.ARPingBinary),
		agent.NewMySQLRoleController(runner, configuration.MySQLBinary, configuration.RoleStateDirectory),
		nil,
	)
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
