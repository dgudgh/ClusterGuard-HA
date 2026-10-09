package maintenance

import (
	"context"
	"fmt"
)

type ConfigurationReader interface{ ConfigurationMaintenanceActive() bool }
type ConfigurationGate struct {
	Base   Gate
	Reader ConfigurationReader
}

func (g ConfigurationGate) Check(ctx context.Context) error {
	if e := g.Base.Check(ctx); e != nil {
		return e
	}
	if g.Reader != nil && g.Reader.ConfigurationMaintenanceActive() {
		return fmt.Errorf("configuration distribution maintenance is active")
	}
	return nil
}
