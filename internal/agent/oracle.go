package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type OracleBrokerStatus struct {
	Configuration       string
	DBID                string
	Database            string
	InstanceName        string
	Role                string
	DatabaseStatus      string
	ConfigurationStatus string
	BrokerEnabled       bool
	ReadyForSwitchover  bool
	TransportLagSeconds *int64
	ApplyLagSeconds     *int64
}

type oracleLocalIdentity struct {
	DBID          string
	Database      string
	InstanceName  string
	Role          string
	OpenMode      string
	BrokerEnabled bool
}

type InputCommandRunner interface {
	RunInput(context.Context, []byte, string, ...string) ([]byte, error)
}

type OSInputCommandRunner struct{}

func (OSInputCommandRunner) RunInput(ctx context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdin = bytes.NewReader(input)
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("local input command failed")
	}
	return output, nil
}

type OracleLocalController struct {
	runner        InputCommandRunner
	setprivBinary string
	lookupUser    func(string) (string, string, []string, error)
}

func NewOracleController(runner InputCommandRunner, setprivBinary string) (*OracleLocalController, error) {
	return newOracleController(runner, setprivBinary, lookupOracleUser)
}

func newOracleController(
	runner InputCommandRunner,
	setprivBinary string,
	lookupUser func(string) (string, string, []string, error),
) (*OracleLocalController, error) {
	setprivBinary = strings.TrimSpace(setprivBinary)
	if runner == nil || lookupUser == nil || !filepath.IsAbs(setprivBinary) {
		return nil, fmt.Errorf("Oracle input runner and absolute privilege-drop executable are required")
	}
	return &OracleLocalController{runner: runner, setprivBinary: setprivBinary, lookupUser: lookupUser}, nil
}

func NewDefaultOracleController(runner InputCommandRunner) (*OracleLocalController, error) {
	return NewOracleController(runner, "/usr/bin/setpriv")
}

func lookupOracleUser(name string) (string, string, []string, error) {
	account, err := user.Lookup(name)
	if err != nil || strings.TrimSpace(account.Uid) == "" || strings.TrimSpace(account.Gid) == "" {
		return "", "", nil, fmt.Errorf("resolve Oracle operating-system user")
	}
	groups, err := account.GroupIds()
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Oracle operating-system groups")
	}
	if len(groups) == 0 {
		groups = []string{account.Gid}
	}
	return account.Uid, account.Gid, groups, nil
}

func (controller *OracleLocalController) dgmgrl(ctx context.Context, policy ClusterPolicy, commands ...string) ([]string, error) {
	password := strings.ReplaceAll(policy.OraclePassword, `"`, `""`)
	authenticationRole := strings.ToUpper(strings.TrimSpace(policy.OracleAuthenticationRole))
	input := []string{
		`CONNECT ` + policy.OracleUsername + `/"` + password + `"@` + policy.OracleConnectIdentifier + ` AS ` + authenticationRole,
	}
	input = append(input, commands...)
	input = append(input, "EXIT")
	uid, gid, groups, err := controller.lookupUser(policy.OracleOSUser)
	if err != nil {
		return nil, fmt.Errorf("Oracle operating-system identity is unavailable")
	}
	arguments := []string{
		"--reuid", uid, "--regid", gid, "--groups", strings.Join(groups, ","), "--",
		"/usr/bin/env",
		"ORACLE_HOME=" + policy.OracleHome,
		"ORACLE_SID=" + policy.OracleSID,
		"PATH=" + filepath.Join(policy.OracleHome, "bin") + ":/usr/bin:/bin",
		policy.OracleDGMGRLBinary, "-silent",
	}
	output, err := controller.runner.RunInput(ctx, []byte(strings.Join(input, "\n")+"\n"), controller.setprivBinary, arguments...)
	if err != nil {
		return nil, fmt.Errorf("Oracle Data Guard Broker command failed")
	}
	lines := make([]string, 0)
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "ORA-") || strings.Contains(upper, "DGM-") || strings.HasPrefix(upper, "ERROR") {
			return nil, fmt.Errorf("Oracle Data Guard Broker reported an error")
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func (controller *OracleLocalController) localIdentity(ctx context.Context, policy ClusterPolicy) (oracleLocalIdentity, error) {
	const query = `set pagesize 0 feedback off verify off heading off echo off
whenever sqlerror exit failure
select to_char(d.dbid)||'|'||d.db_unique_name||'|'||i.instance_name||'|'||d.database_role||'|'||d.open_mode||'|'||
       (select value from v$parameter where name='dg_broker_start')
  from v$database d cross join v$instance i;
exit
`
	uid, gid, groups, err := controller.lookupUser(policy.OracleOSUser)
	if err != nil {
		return oracleLocalIdentity{}, fmt.Errorf("Oracle operating-system identity is unavailable")
	}
	sqlplus := filepath.Join(filepath.Clean(policy.OracleHome), "bin", "sqlplus")
	arguments := []string{
		"--reuid", uid, "--regid", gid, "--groups", strings.Join(groups, ","), "--",
		"/usr/bin/env",
		"ORACLE_HOME=" + policy.OracleHome,
		"ORACLE_SID=" + policy.OracleSID,
		"PATH=" + filepath.Join(policy.OracleHome, "bin") + ":/usr/bin:/bin",
		sqlplus, "-s", "-L", "/", "as", "sysdba",
	}
	output, err := controller.runner.RunInput(ctx, []byte(query), controller.setprivBinary, arguments...)
	if err != nil {
		return oracleLocalIdentity{}, fmt.Errorf("Oracle local identity query failed")
	}
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		parts := strings.Split(line, "|")
		if len(parts) != 6 {
			continue
		}
		for index := range parts {
			parts[index] = strings.TrimSpace(parts[index])
		}
		if _, err := strconv.ParseUint(parts[0], 10, 64); err != nil ||
			!oracleNamePattern.MatchString(parts[1]) || !oracleNamePattern.MatchString(parts[2]) ||
			parts[3] == "" || parts[4] == "" {
			continue
		}
		return oracleLocalIdentity{
			DBID: parts[0], Database: parts[1], InstanceName: parts[2],
			Role: strings.ToUpper(parts[3]), OpenMode: strings.ToUpper(parts[4]),
			BrokerEnabled: strings.EqualFold(parts[5], "true"),
		}, nil
	}
	return oracleLocalIdentity{}, fmt.Errorf("Oracle local identity query returned incomplete evidence")
}

func (controller *OracleLocalController) Discover(ctx context.Context, policy ClusterPolicy) (OracleBrokerStatus, error) {
	lines, err := controller.dgmgrl(
		ctx, policy,
		"SHOW CONFIGURATION",
		"SHOW DATABASE VERBOSE "+policy.OracleDatabaseUniqueName,
	)
	if err != nil {
		return OracleBrokerStatus{}, err
	}
	identity, err := controller.localIdentity(ctx, policy)
	if err != nil {
		return OracleBrokerStatus{}, err
	}
	status := parseOracleBrokerStatus(lines)
	status.DBID = identity.DBID
	status.Database = identity.Database
	status.InstanceName = identity.InstanceName
	status.BrokerEnabled = identity.BrokerEnabled
	if status.Role == "" {
		status.Role = identity.Role
	}
	if !strings.EqualFold(status.Configuration, policy.OracleBrokerConfiguration) ||
		!strings.EqualFold(status.Database, policy.OracleDatabaseUniqueName) ||
		!strings.EqualFold(status.Role, identity.Role) ||
		status.DBID == "" || status.Role == "" || status.ConfigurationStatus == "" || status.DatabaseStatus == "" {
		return OracleBrokerStatus{}, fmt.Errorf("Oracle Data Guard Broker returned incomplete discovery evidence")
	}
	return status, nil
}

func (controller *OracleLocalController) Status(ctx context.Context, policy ClusterPolicy, target string) (OracleBrokerStatus, error) {
	localLines, err := controller.dgmgrl(
		ctx, policy,
		"SHOW CONFIGURATION",
		"SHOW DATABASE VERBOSE "+policy.OracleDatabaseUniqueName,
	)
	if err != nil {
		return OracleBrokerStatus{}, err
	}
	targetLines, err := controller.dgmgrl(ctx, policy, "VALIDATE DATABASE VERBOSE "+target)
	if err != nil {
		return OracleBrokerStatus{}, err
	}
	status := parseOracleBrokerStatus(localLines)
	targetStatus := parseOracleBrokerStatus(targetLines)
	status.Database = policy.OracleDatabaseUniqueName
	status.InstanceName = policy.OracleSID
	status.BrokerEnabled = true
	status.ReadyForSwitchover = targetStatus.ReadyForSwitchover
	status.TransportLagSeconds = targetStatus.TransportLagSeconds
	status.ApplyLagSeconds = targetStatus.ApplyLagSeconds
	if !strings.EqualFold(status.Configuration, policy.OracleBrokerConfiguration) ||
		status.Role == "" || status.ConfigurationStatus == "" || status.DatabaseStatus == "" {
		return OracleBrokerStatus{}, fmt.Errorf("Oracle Data Guard Broker returned incomplete status evidence")
	}
	return status, nil
}

func (controller *OracleLocalController) Switchover(ctx context.Context, policy ClusterPolicy, target string) error {
	status, err := controller.Status(ctx, policy, target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(status.Role, "PRIMARY") ||
		!strings.HasPrefix(strings.ToUpper(status.ConfigurationStatus), "SUCCESS") ||
		!status.ReadyForSwitchover {
		return fmt.Errorf("Oracle Data Guard Broker precheck did not permit switchover")
	}
	lines, err := controller.dgmgrl(
		ctx, policy,
		"SHOW CONFIGURATION",
		"SWITCHOVER TO "+target,
		"SHOW CONFIGURATION",
	)
	if err != nil {
		return err
	}
	joined := strings.ToUpper(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "SWITCHOVER SUCCEEDED") && !strings.Contains(joined, "SWITCHOVER SUCCESSFUL") {
		return fmt.Errorf("Oracle Data Guard Broker did not confirm switchover completion")
	}
	result := parseOracleBrokerStatus(lines)
	if !strings.EqualFold(result.Configuration, policy.OracleBrokerConfiguration) ||
		!strings.HasPrefix(strings.ToUpper(result.ConfigurationStatus), "SUCCESS") {
		return fmt.Errorf("Oracle Data Guard Broker configuration is not healthy after switchover")
	}
	return nil
}

func parseOracleBrokerStatus(lines []string) OracleBrokerStatus {
	status := OracleBrokerStatus{}
	expectConfigurationStatus := false
	expectDatabaseStatus := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(line), "CONFIGURATION - ") {
			status.Configuration = strings.TrimSpace(line[len("Configuration - "):])
			continue
		}
		if expectConfigurationStatus {
			status.ConfigurationStatus = strings.Fields(line)[0]
			expectConfigurationStatus = false
			continue
		}
		if expectDatabaseStatus {
			status.DatabaseStatus = strings.ToUpper(strings.Fields(line)[0])
			expectDatabaseStatus = false
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), " ", "")) {
		case "databaseid", "dbid":
			status.DBID = strings.TrimSpace(value)
		case "configurationstatus":
			value = strings.TrimSpace(value)
			if value == "" {
				expectConfigurationStatus = true
			} else {
				status.ConfigurationStatus = strings.Fields(value)[0]
			}
		case "role", "databaserole":
			status.Role = strings.ToUpper(strings.TrimSpace(value))
		case "databasestatus":
			value = strings.TrimSpace(value)
			if value == "" {
				expectDatabaseStatus = true
			} else {
				status.DatabaseStatus = strings.ToUpper(value)
			}
		case "readyforswitchover":
			status.ReadyForSwitchover = strings.EqualFold(strings.TrimSpace(value), "yes")
		case "transportlag":
			status.TransportLagSeconds = parseOracleAgentLag(value)
		case "applylag":
			status.ApplyLagSeconds = parseOracleAgentLag(value)
		}
	}
	return status
}

var oracleAgentLagPattern = regexp.MustCompile(`(?i)(\d+)\s*(day|days|hour|hours|minute|minutes|second|seconds)`)

func parseOracleAgentLag(raw string) *int64 {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, "unknown") {
		return nil
	}
	if strings.HasPrefix(value, "+") {
		parts := strings.Fields(strings.TrimPrefix(value, "+"))
		if len(parts) == 2 {
			clock := strings.Split(parts[1], ":")
			if len(clock) == 3 {
				days, dayErr := strconv.ParseInt(parts[0], 10, 64)
				hours, hourErr := strconv.ParseInt(clock[0], 10, 64)
				minutes, minuteErr := strconv.ParseInt(clock[1], 10, 64)
				seconds, secondErr := strconv.ParseInt(clock[2], 10, 64)
				if dayErr == nil && hourErr == nil && minuteErr == nil && secondErr == nil {
					total := days*86400 + hours*3600 + minutes*60 + seconds
					return &total
				}
			}
		}
	}
	var total int64
	found := false
	for _, match := range oracleAgentLagPattern.FindAllStringSubmatch(value, -1) {
		number, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		found = true
		switch strings.ToLower(match[2]) {
		case "day", "days":
			total += number * 86400
		case "hour", "hours":
			total += number * 3600
		case "minute", "minutes":
			total += number * 60
		default:
			total += number
		}
	}
	if !found {
		return nil
	}
	return &total
}
