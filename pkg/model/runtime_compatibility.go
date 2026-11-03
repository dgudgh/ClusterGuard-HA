package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// RuntimeCompatibilityFloor is durable and monotonic. Protocol protects the
// mechanism itself; understanding a policy without enforcing its floor is not
// sufficient to run after that policy has been persisted.
type RuntimeCompatibilityFloor struct {
	Protocol               int `json:"protocol"`
	InstanceFailoverPolicy int `json:"instance_failover_policy"`
}

const RuntimeCompatibilityProtocol = 1
const InstanceFailoverPolicyProtocol = 1

func PolicyCompatibilityFloor() RuntimeCompatibilityFloor {
	return RuntimeCompatibilityFloor{RuntimeCompatibilityProtocol, InstanceFailoverPolicyProtocol}
}
func (f RuntimeCompatibilityFloor) Required() bool {
	return f.Protocol != 0 || f.InstanceFailoverPolicy != 0
}
func (f RuntimeCompatibilityFloor) Valid() bool {
	return f == (RuntimeCompatibilityFloor{}) || f == PolicyCompatibilityFloor()
}
func (f RuntimeCompatibilityFloor) Covers(other RuntimeCompatibilityFloor) bool {
	return f.Valid() && other.Valid() && f.Protocol >= other.Protocol && f.InstanceFailoverPolicy >= other.InstanceFailoverPolicy
}

func (f *RuntimeCompatibilityFloor) UnmarshalJSON(data []byte) error {
	type plain RuntimeCompatibilityFloor
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid compatibility floor JSON")
	}
	*f = RuntimeCompatibilityFloor(v)
	return nil
}
