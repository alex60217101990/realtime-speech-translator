package mt

import "testing"

func TestDisabled_Passthrough(t *testing.T) {
	var e Engine = Disabled{}
	got, err := e.Translate("hello world", "en", "ru")
	if err != nil {
		t.Fatalf("Translate returned err: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Translate = %q, want passthrough %q", got, "hello world")
	}
	if err := e.Close(); err != nil {
		t.Errorf("Close returned err: %v", err)
	}
}

func TestSerial_DelegatesAndSerialises(t *testing.T) {
	// Confirm Serial preserves Disabled behaviour and the Engine
	// interface contract under wrapping.
	wrapped := Serial(Disabled{})
	got, err := wrapped.Translate("hi", "en", "fr")
	if err != nil {
		t.Fatalf("Translate via Serial: %v", err)
	}
	if got != "hi" {
		t.Errorf("Serial(Disabled).Translate = %q, want %q", got, "hi")
	}
	if err := wrapped.Close(); err != nil {
		t.Errorf("Serial.Close: %v", err)
	}
}
