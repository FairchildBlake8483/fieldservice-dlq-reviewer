package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
)

const fieldServiceQueue = "field-service-jobs"

type WorkOrder struct {
	WorkOrderID        string   `json:"work_order_id"`
	PhotoURLs          []string `json:"photo_urls"`
	DispatchStatus     string   `json:"dispatch_status"`
	TechnicianFollowUp bool     `json:"technician_follow_up"`
}

type DeadLetter struct {
	MessageID string    `json:"message_id"`
	Payload   WorkOrder `json:"payload"`
}

type DLQ interface {
	ListDeadLetters(context.Context, string) ([]DeadLetter, error)
	Redrive(context.Context, string, string) error
}

type RedriveDecision struct {
	WorkOrderID string `json:"work_order_id"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
}

func DecideRedrive(order WorkOrder) RedriveDecision {
	decision := RedriveDecision{WorkOrderID: order.WorkOrderID}
	switch {
	case order.WorkOrderID == "":
		decision.Action, decision.Reason = "hold", "missing work order identity"
	case order.DispatchStatus != "technician_follow_up_complete":
		decision.Action, decision.Reason = "hold", "technician follow-up is still open"
	case order.TechnicianFollowUp:
		decision.Action, decision.Reason = "hold", "technician follow-up is still required"
	case len(order.PhotoURLs) == 0:
		decision.Action, decision.Reason = "hold", "work-order photos are missing"
	default:
		decision.Action, decision.Reason = "redrive", "job evidence and follow-up are complete"
	}
	return decision
}

type service struct {
	dlq   DLQ
	queue string
}

func (s service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dead-letters", s.list)
	mux.HandleFunc("POST /dead-letters/{workOrderID}/redrive", s.redrive)
	return mux
}

func (s service) list(w http.ResponseWriter, r *http.Request) {
	items, err := s.dlq.ListDeadLetters(r.Context(), s.queue)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	type review struct {
		DeadLetter DeadLetter      `json:"dead_letter"`
		Decision   RedriveDecision `json:"decision"`
	}
	result := make([]review, 0, len(items))
	for _, item := range items {
		result = append(result, review{DeadLetter: item, Decision: DecideRedrive(item.Payload)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (s service) redrive(w http.ResponseWriter, r *http.Request) {
	wanted := r.PathValue("workOrderID")
	items, err := s.dlq.ListDeadLetters(r.Context(), s.queue)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	for _, item := range items {
		if item.Payload.WorkOrderID != wanted {
			continue
		}
		decision := DecideRedrive(item.Payload)
		if decision.Action != "redrive" {
			writeJSON(w, http.StatusConflict, decision)
			return
		}
		if err := s.dlq.Redrive(r.Context(), s.queue, item.MessageID); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decision)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "work order not found in dead letters"})
}

func writeServiceError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var apiErr *InfraiError
	if errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
		status = apiErr.HTTPStatus
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	handler := service{dlq: NewQueueClient(key), queue: fieldServiceQueue}.routes()
	log.Printf("field-service DLQ reviewer listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(fmt.Errorf("serve: %w", err))
	}
}
