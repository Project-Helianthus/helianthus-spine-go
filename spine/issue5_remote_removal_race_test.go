package spine

import (
	"sync"
	"testing"

	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestIssue5RemoteLookupAndRemovalShareOneLockDiscipline(t *testing.T) {
	device := NewDeviceLocal(
		"brand",
		"model",
		"serial",
		"code",
		"address",
		model.DeviceTypeTypeEnergyManagementSystem,
		model.NetworkManagementFeatureSetTypeSmart,
	)
	const ski = "issue-5"
	remote := NewDeviceRemote(device, ski, NewSender(&issue5ShipWriter{}))

	start := make(chan struct{})
	var readers sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for lookup := 0; lookup < 2_000; lookup++ {
				_ = device.RemoteDeviceForSki(ski)
			}
		}()
	}

	close(start)
	for iteration := 0; iteration < 500; iteration++ {
		device.AddRemoteDeviceForSki(ski, remote)
		device.RemoveRemoteDevice(ski)
	}
	readers.Wait()
}

type issue5ShipWriter struct{}

func (*issue5ShipWriter) WriteShipMessageWithPayload([]byte) {}
