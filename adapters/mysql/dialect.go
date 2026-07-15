package mysql

import (
	"fmt"
	"strconv"
	"strings"
)

type mysqlDialect struct {
	StopReplication       string
	ResetReplication      string
	StartReplication      string
	PublicKeySourceOption string
	ModernSource          bool
}

func numericVersionComponent(value string) (int, error) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("version component %q is not numeric", value)
	}
	return strconv.Atoi(value[:end])
}

func dialectForVersion(version string) (mysqlDialect, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	major, err := numericVersionComponent(parts[0])
	if err != nil {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	minor, err := numericVersionComponent(parts[1])
	if err != nil {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	if major < 5 || (major == 5 && minor < 7) {
		return mysqlDialect{}, fmt.Errorf("MySQL version %q is unsupported for guarded switchover", version)
	}
	if major == 5 {
		return mysqlDialect{StopReplication: "STOP SLAVE", ResetReplication: "RESET SLAVE ALL", StartReplication: "START SLAVE"}, nil
	}
	if major == 8 && minor == 0 {
		if len(parts) < 3 {
			return mysqlDialect{}, fmt.Errorf("MySQL version %q does not identify the replication syntax boundary", version)
		}
		patch, err := numericVersionComponent(parts[2])
		if err != nil {
			return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
		}
		if patch < 22 {
			return mysqlDialect{
				StopReplication: "STOP SLAVE", ResetReplication: "RESET SLAVE ALL", StartReplication: "START SLAVE",
				PublicKeySourceOption: "GET_MASTER_PUBLIC_KEY=1",
			}, nil
		}
	}
	return mysqlDialect{
		StopReplication: "STOP REPLICA", ResetReplication: "RESET REPLICA ALL", StartReplication: "START REPLICA",
		PublicKeySourceOption: "GET_SOURCE_PUBLIC_KEY=1", ModernSource: true,
	}, nil
}
