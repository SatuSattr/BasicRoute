package service

import "testing"

func TestProvHealthProgress(t *testing.T) {
	h := NewProvHealth()
	id := ProvProviderID("oc")

	h.SetProgress(id, ProvPhaseFetching, 0, 0)
	if !h.IsTesting(id) {
		t.Fatal("SetProgress must mark testing (yellow)")
	}
	if phase, done, total, ok := h.Progress(id); !ok || phase != ProvPhaseFetching || done != 0 || total != 0 {
		t.Fatalf("progress = %q %d/%d ok=%v", phase, done, total, ok)
	}

	h.SetProgress(id, ProvPhaseIdentifying, 3, 8)
	if phase, done, total, ok := h.Progress(id); !ok || phase != ProvPhaseIdentifying || done != 3 || total != 8 {
		t.Fatalf("progress = %q %d/%d ok=%v", phase, done, total, ok)
	}

	h.SetOK(id)
	if _, _, _, ok := h.Progress(id); ok {
		t.Fatal("SetOK must clear progress")
	}
	if h.IsTesting(id) {
		t.Fatal("SetOK must clear testing")
	}

	// SetTesting starts a fresh run: stale progress is dropped.
	h.SetProgress(id, ProvPhaseIdentifying, 1, 2)
	h.SetTesting(id)
	if _, _, _, ok := h.Progress(id); ok {
		t.Fatal("SetTesting must clear stale progress")
	}

	// SetError also clears progress.
	h.SetProgress(id, ProvPhaseIdentifying, 1, 2)
	h.SetError(id, "boom")
	if _, _, _, ok := h.Progress(id); ok {
		t.Fatal("SetError must clear progress")
	}
	if !h.IsError(id) {
		t.Fatal("SetError must flag error")
	}

	// Negative done is clamped, nil receiver is safe.
	h.SetProgress(id, ProvPhaseIdentifying, -5, 4)
	if _, done, _, _ := h.Progress(id); done != 0 {
		t.Fatalf("negative done must clamp to 0, got %d", done)
	}
	var nilH *ProvHealth
	nilH.SetProgress("x", ProvPhaseFetching, 0, 0)
	if _, _, _, ok := nilH.Progress("x"); ok {
		t.Fatal("nil receiver progress must be empty")
	}
}
