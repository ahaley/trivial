package store

import "testing"

func TestAISettingDefaultsOnAndRoundTrips(t *testing.T) {
	st := openTest(t)

	on, err := st.AIEnabled()
	if err != nil {
		t.Fatalf("AIEnabled: %v", err)
	}
	if !on {
		t.Fatal("a fresh store should default to AI grading on")
	}

	if err := st.SetAIEnabled(false); err != nil {
		t.Fatalf("SetAIEnabled(false): %v", err)
	}
	if on, _ := st.AIEnabled(); on {
		t.Fatal("setting should read back as off")
	}

	// A second write exercises the upsert path.
	if err := st.SetAIEnabled(true); err != nil {
		t.Fatalf("SetAIEnabled(true): %v", err)
	}
	if on, _ := st.AIEnabled(); !on {
		t.Fatal("setting should read back as on again")
	}
}
