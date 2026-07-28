package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
)

func (s *Server) handleListTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.store.ListTopics()
	if writeStoreError(w, err, "topics") {
		return
	}
	if topics == nil {
		topics = []store.TopicSummary{}
	}
	writeJSON(w, http.StatusOK, topics)
}

type createTopicRequest struct {
	Subject     string `json:"subject"`
	Guidance    string `json:"guidance"`
	TargetCount int    `json:"target_count"`
}

func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	var req createTopicRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" {
		writeError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if len(req.Subject) > 200 {
		writeError(w, http.StatusBadRequest, "subject is too long")
		return
	}
	if req.TargetCount <= 0 {
		req.TargetCount = s.cfg.TargetCount
	}
	if req.TargetCount > 100 {
		req.TargetCount = 100
	}

	topic, err := s.store.CreateTopic(req.Subject, strings.TrimSpace(req.Guidance), req.TargetCount)
	if writeStoreError(w, err, "topic") {
		return
	}

	s.startGeneration(topic)

	// Summary rather than the bare topic, so the client's list entry has the
	// same shape whether it came from here or from GET /topics.
	summary, err := s.store.TopicSummaryByID(topic.ID)
	if writeStoreError(w, err, "topic") {
		return
	}
	writeJSON(w, http.StatusAccepted, summary)
}

// startGeneration runs the pipeline in the background.
//
// It uses the server's own context rather than the request's: generation
// outlives the request that asked for it, and a client that navigates away must
// not cancel a job that has already started spending tokens.
func (s *Server) startGeneration(topic model.Topic) {
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		if err := s.gen.Run(s.baseCtx, topic); err != nil {
			s.log.Error("generation job ended in failure", "topic", topic.ID, "err", err)
		}
	}()
}

// handleRegenerate rebuilds a topic's deck from the same subject. It exists so
// a generation that failed for a passing reason — a bad model ID, an expired
// credential — is recoverable without losing the topic and retyping it.
func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid topic id")
		return
	}

	topic, err := s.store.ResetForRegeneration(id)
	if errors.Is(err, store.ErrBusy) {
		writeError(w, http.StatusConflict, "this topic is already generating")
		return
	}
	if writeStoreError(w, err, "topic") {
		return
	}

	s.log.Info("regenerating topic", "topic", topic.ID, "subject", topic.Subject)
	s.startGeneration(topic)

	// Return the summary, not the bare topic: the client replaces its copy with
	// this, and the deck counts have just been reset to zero.
	summary, err := s.store.TopicSummaryByID(topic.ID)
	if writeStoreError(w, err, "topic") {
		return
	}
	writeJSON(w, http.StatusAccepted, summary)
}

// topicDetail is the topic page payload.
type topicDetail struct {
	store.TopicSummary
	Facts []model.Fact `json:"facts"`
	// Modes counts how many facts carry each rendering.
	Modes map[model.Mode]int `json:"modes"`
	// Subtopics counts facts per tag, for the deck breakdown.
	Subtopics []tagCount `json:"subtopics"`
}

type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

func (s *Server) handleGetTopic(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid topic id")
		return
	}
	summary, err := s.store.TopicSummaryByID(id)
	if writeStoreError(w, err, "topic") {
		return
	}
	facts, err := s.store.ListFacts(id)
	if writeStoreError(w, err, "facts") {
		return
	}

	detail := topicDetail{TopicSummary: summary, Facts: facts, Modes: map[model.Mode]int{}}
	tags := map[string]int{}
	for _, f := range facts {
		for _, q := range f.Questions {
			detail.Modes[q.Mode]++
		}
		for _, t := range f.Tags {
			tags[t]++
		}
	}
	for tag, n := range tags {
		detail.Subtopics = append(detail.Subtopics, tagCount{Tag: tag, Count: n})
	}
	sortTagCounts(detail.Subtopics)
	if detail.Facts == nil {
		detail.Facts = []model.Fact{}
	}
	writeJSON(w, http.StatusOK, detail)
}

// sortTagCounts orders subtopics by frequency, then alphabetically.
func sortTagCounts(in []tagCount) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0; j-- {
			a, b := in[j-1], in[j]
			if a.Count > b.Count || (a.Count == b.Count && a.Tag <= b.Tag) {
				break
			}
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}

func (s *Server) handleDeleteTopic(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid topic id")
		return
	}
	if writeStoreError(w, s.store.DeleteTopic(id), "topic") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generationStatus is what the creation screen polls.
type generationStatus struct {
	TopicID   int64             `json:"topic_id"`
	Status    model.TopicStatus `json:"status"`
	Stage     string            `json:"stage"`
	Progress  int               `json:"progress"`
	Error     string            `json:"error,omitempty"`
	FactCount int               `json:"fact_count"`
	Target    int               `json:"target_count"`
}

func (s *Server) handleGeneration(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid topic id")
		return
	}
	summary, err := s.store.TopicSummaryByID(id)
	if writeStoreError(w, err, "topic") {
		return
	}
	writeJSON(w, http.StatusOK, generationStatus{
		TopicID:   summary.ID,
		Status:    summary.Status,
		Stage:     summary.Stage,
		Progress:  summary.Progress,
		Error:     summary.Error,
		FactCount: summary.FactCount,
		Target:    summary.TargetCount,
	})
}
