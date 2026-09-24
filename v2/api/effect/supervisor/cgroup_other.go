//go:build !linux

package supervisor

import (
	"context"
	"fmt"

	"norn/v2/api/effect"
)

func NewCgroupBackend(_, _, _ string, _ []byte) (Backend, error) {
	return nil, fmt.Errorf("cgroup-v2 effect containment is supported only on Linux; this platform must remain fail closed until separately qualified")
}

func (b *cgroupBackend) Start(context.Context, BackendExecution, effect.LaunchMaterial) error {
	return fmt.Errorf("cgroup-v2 effect containment is supported only on Linux")
}

func (b *cgroupBackend) StartSnapshot(context.Context, BackendExecution, SnapshotDescriptor, SnapshotLaunchMaterial) error {
	return fmt.Errorf("cgroup-v2 snapshot containment is supported only on Linux")
}
