package controller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dakshjotwani/gru/internal/controller"
)

type fakeController struct {
	runtimeID    string
	capabilities []controller.Capability
}

func (f *fakeController) RuntimeID() string                        { return f.runtimeID }
func (f *fakeController) Capabilities() []controller.Capability   { return f.capabilities }
func (f *fakeController) Launch(_ context.Context, _ controller.LaunchOptions) (*controller.SessionHandle, error) {
	return nil, errors.New("fake: not implemented")
}

func TestControllerRegistry_RegisterAndGet(t *testing.T) {
	reg := controller.NewRegistry()
	fc := &fakeController{runtimeID: "fake-runtime", capabilities: []controller.Capability{controller.CapKill}}
	reg.Register(fc)
	got, err := reg.Get("fake-runtime")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.RuntimeID() != "fake-runtime" {
		t.Errorf("RuntimeID = %q, want %q", got.RuntimeID(), "fake-runtime")
	}
}

func TestControllerRegistry_GetUnknown(t *testing.T) {
	reg := controller.NewRegistry()
	_, err := reg.Get("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown runtime, got nil")
	}
}

func TestControllerRegistry_RegisterDuplicate(t *testing.T) {
	reg := controller.NewRegistry()
	fc := &fakeController{runtimeID: "dup"}
	reg.Register(fc)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register, got none")
		}
	}()
	reg.Register(fc)
}
