package mysql

import (
	"fmt"
	"strconv"
	"strings"
)

type mysqlDialect struct {
	StopReplication  string
	ResetReplication string
}

func dialectForVersion(version string) (mysqlDialect, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return mysqlDialect{}, fmt.Errorf("invalid MySQL version %q", version)
	}
	if major < 5 || (major == 5 && minor < 7) {
		return mysqlDialect{}, fmt.Errorf("MySQL version %q is unsupported for guarded switchover", version)
	}
	if major == 5 {
		return mysqlDialect{StopReplication: "STOP SLAVE", ResetReplication: "RESET SLAVE ALL"}, nil
	}
	return mysqlDialect{StopReplication: "STOP REPLICA", ResetReplication: "RESET REPLICA ALL"}, nil
}
