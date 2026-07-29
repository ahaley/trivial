package server

import "net/http"

// settingsView is the settings object the API exchanges in both directions.
type settingsView struct {
	// AIEnabled controls whether open-ended answers are graded by the model.
	// Off means instant word-overlap grading; explanations stay model-backed.
	AIEnabled bool `json:"ai_enabled"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	on, err := s.store.AIEnabled()
	if writeStoreError(w, err, "settings") {
		return
	}
	writeJSON(w, http.StatusOK, settingsView{AIEnabled: on})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	// Pointer field so an absent key is distinguishable from false: a stray
	// empty object must not silently switch AI grading off.
	var req struct {
		AIEnabled *bool `json:"ai_enabled"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.AIEnabled == nil {
		writeError(w, http.StatusBadRequest, "ai_enabled is required")
		return
	}
	if err := s.store.SetAIEnabled(*req.AIEnabled); writeStoreError(w, err, "settings") {
		return
	}
	writeJSON(w, http.StatusOK, settingsView{AIEnabled: *req.AIEnabled})
}
