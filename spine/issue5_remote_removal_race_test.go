package spine

import (
	"sync"
	"testing"
	"time"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/mocks"
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

func TestIssue5ReplacementWaitsForPriorRemoteCleanup(t *testing.T) {
	device := NewDeviceLocal(
		"brand",
		"model",
		"serial",
		"code",
		"address",
		model.DeviceTypeTypeEnergyManagementSystem,
		model.NetworkManagementFeatureSetTypeSmart,
	)
	const ski = "issue-5-replacement"
	writer := &issue5ShipWriter{}
	oldRemote := NewDeviceRemote(device, ski, NewSender(writer))
	replacement := NewDeviceRemote(device, ski, NewSender(writer))
	device.AddRemoteDeviceForSki(ski, oldRemote)

	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	subscriptions := mocks.NewSubscriptionManagerInterface(t)
	subscriptions.EXPECT().RemoveSubscriptionsForDevice(oldRemote).Run(func(spineapi.DeviceRemoteInterface) {
		close(cleanupEntered)
		<-releaseCleanup
	}).Once()
	bindings := mocks.NewBindingManagerInterface(t)
	bindings.EXPECT().RemoveBindingsForDevice(oldRemote).Once()
	device.subscriptionManager = subscriptions
	device.bindingManager = bindings

	removeDone := make(chan struct{})
	go func() {
		defer close(removeDone)
		device.RemoveRemoteDevice(ski)
	}()
	<-cleanupEntered

	addDone := make(chan struct{})
	go func() {
		defer close(addDone)
		device.AddRemoteDeviceForSki(ski, replacement)
	}()
	select {
	case <-addDone:
		t.Fatal("replacement became visible before prior remote cleanup completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseCleanup)
	<-removeDone
	<-addDone
	if got := device.RemoteDeviceForSki(ski); got != replacement {
		t.Fatalf("remote after serialized replacement = %T %p, want replacement %p", got, got, replacement)
	}
}

type issue5ShipWriter struct{}

func (*issue5ShipWriter) WriteShipMessageWithPayload([]byte) {}
