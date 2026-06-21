package tuya

import (
	"context"
	"errors"
	"testing"

	"go.naturallyfunny.dev/tuya"
	"go.naturallyfunny.dev/tuya/cloud"
)

// stubClient implements Client for tests that only need tool registration to succeed.
type stubClient struct{}

func (stubClient) Account(context.Context, string) (tuya.Account, error)       { return tuya.Account{}, nil }
func (stubClient) ListDevices(context.Context, string) ([]cloud.Device, error) { return nil, nil }
func (stubClient) DeviceStatus(context.Context, string, string) ([]cloud.DataPoint, error) {
	return nil, nil
}
func (stubClient) SendCommands(context.Context, string, string, []cloud.DataPoint) error { return nil }

func TestToolsNilClient(t *testing.T) {
	if _, err := Tools(nil); err == nil {
		t.Fatal("Tools(nil): want error, got nil")
	}
}

func TestToolsNames(t *testing.T) {
	tools, err := Tools(stubClient{})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	want := []string{"get_account", "list_devices", "device_status", "send_commands"}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestForAgent(t *testing.T) {
	passthrough := errors.New("some other failure")
	tests := []struct {
		name string
		err  error
		want string // substring the translated error must contain
	}{
		{"not linked", tuya.ErrAccountNotLinked, "hasn't linked"},
		{"not owned", tuya.ErrDeviceNotOwned, "isn't on the human's Tuya account"},
		{"passthrough", passthrough, "some other failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forAgent(tt.err)
			if got == nil || !contains(got.Error(), tt.want) {
				t.Errorf("forAgent(%v) = %v, want substring %q", tt.err, got, tt.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
