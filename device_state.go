package adb

// DeviceState represents one of the 3 possible states adb will report devices.
// A device can be communicated with when it's in StateOnline.
// A USB device will make the following state transitions:
// 	Plugged in: StateDisconnected->StateOffline->StateOnline
// 	Unplugged:  StateOnline->StateDisconnected
//go:generate stringer -type=DeviceState
type DeviceState int8

const (
	StateInvalid DeviceState = iota
	StateDisconnected
	StateOffline
	StateOnline
)

var deviceStateStrings = map[string]DeviceState{
	"":        StateDisconnected,
	"offline": StateOffline,
	"device":  StateOnline,
}

func parseDeviceState(str string) (DeviceState, error) {
	state, _ := deviceStateStrings[str]
	return state, nil
}
