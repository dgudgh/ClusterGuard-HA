package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"clusterguard.io/ha/internal/agent"
)

type runtimeControllers struct {
	vip        agent.VIPController
	roles      agent.RoleController
	postgresql agent.PostgreSQLController
	oracle     agent.OracleController
	power      agent.PowerController
}

func newRuntimeControllers(configuration agent.Config, runner agent.CommandRunner) (runtimeControllers, error) {
	if configuration.DockerConfigDirectory != "" {
		if err := os.MkdirAll(configuration.DockerConfigDirectory, 0o750); err != nil {
			return runtimeControllers{}, fmt.Errorf("create Docker CLI configuration directory: %w", err)
		}
	}
	linuxPostgreSQL, err := agent.NewDefaultPostgreSQLController(runner)
	if err != nil {
		return runtimeControllers{}, err
	}
	dockerPostgreSQL, err := agent.NewDockerPostgreSQLController(runner, configuration.DockerBinary, configuration.DockerConfigDirectory)
	if err != nil {
		return runtimeControllers{}, err
	}
	oracle, err := agent.NewDefaultOracleController(agent.OSInputCommandRunner{})
	if err != nil {
		return runtimeControllers{}, err
	}
	linuxRoles := agent.NewMySQLRoleController(runner, configuration.MySQLBinary, configuration.RoleStateDirectory)
	dockerRoles := agent.NewDockerMySQLRoleController(runner, configuration.DockerBinary, configuration.RoleStateDirectory, configuration.DockerConfigDirectory)
	return runtimeControllers{
		vip:        agent.NewLinuxVIPController(runner, configuration.IPBinary, configuration.ARPingBinary),
		roles:      agent.NewRuntimeRoleController(linuxRoles, dockerRoles),
		postgresql: agent.NewRuntimePostgreSQLController(linuxPostgreSQL, dockerPostgreSQL),
		oracle:     oracle,
		power: agent.NewRuntimePowerController(
			agent.NewLinuxPowerController(runner),
			agent.NewDockerPowerController(runner, configuration.DockerBinary, dockerRoles),
		),
	}, nil
}

func newRuntimeMutationLedger(configuration agent.Config) (agent.MutationLedger, error) {
	return agent.NewFileMutationLedger(configuration.MutationStateDirectory)
}

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
	controllers, err := newRuntimeControllers(configuration, runner)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
		decisionCache, err := agent.NewFileReconcileDecisionCache(configuration.DecisionStateDirectory)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		decisions, err := agent.NewHTTPReconcileClient(
			configuration.ControllerURLs, configuration.SharedSecret, httpClient, configuration.AllowInsecureHTTP, nil,
			agent.WithReconcileDecisionCache(decisionCache),
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		results, reconcileErr := agent.NewReconciler(
			controllers.vip, controllers.roles, decisions,
			agent.WithPostgreSQLReconcileController(controllers.postgresql),
		).ReconcileAll(context.Background(), configuration.Clusters)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"status": "completed", "results": results})
		if reconcileErr != nil {
			fmt.Fprintln(os.Stderr, reconcileErr)
			os.Exit(3)
		}
		return
	}
	mutationLedger, err := newRuntimeMutationLedger(configuration)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	service, err := agent.NewService(
		configuration, controllers.vip, controllers.roles, nil,
		agent.WithPostgreSQLController(controllers.postgresql),
		agent.WithOracleController(controllers.oracle),
		agent.WithPowerController(controllers.power),
		agent.WithMutationLedger(mutationLedger),
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
