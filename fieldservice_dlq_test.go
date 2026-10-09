package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecideRedrive(t *testing.T) {
	tests := []struct {
		name string
		job  WorkOrder
		want string
	}{
		{
			name: "complete evidence returns job to dispatch",
			job:  WorkOrder{WorkOrderID: "WO-1842", PhotoURLs: []string{"https://photos.example/arrival.jpg"}, DispatchStatus: "technician_follow_up_complete"},
			want: "redrive",
		},
		{
			name: "open follow-up stays in review",
			job:  WorkOrder{WorkOrderID: "WO-1843", PhotoURLs: []string{"https://photos.example/arrival.jpg"}, DispatchStatus: "technician_follow_up_complete", TechnicianFollowUp: true},
			want: "hold",
		},
		{
			name: "missing photo stays in review",
			job:  WorkOrder{WorkOrderID: "WO-1844", DispatchStatus: "technician_follow_up_complete"},
			want: "hold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecideRedrive(tt.job); got.Action != tt.want {
				t.Fatalf("DecideRedrive() action = %q, want %q", got.Action, tt.want)
			}
		})
	}
}

type recordingDLQ struct {
	items    []DeadLetter
	redriven []string
}

func (d *recordingDLQ) ListDeadLetters(context.Context, string) ([]DeadLetter, error) {
	return d.items, nil
}

func (d *recordingDLQ) Redrive(_ context.Context, _ string, messageID string) error {
	d.redriven = append(d.redriven, messageID)
	return nil
}

func TestRedriveEndpointAppliesEligibleDecision(t *testing.T) {
	dlq := &recordingDLQ{items: []DeadLetter{{
		MessageID: "msg-17",
		Payload: WorkOrder{
			WorkOrderID:    "WO-1842",
			PhotoURLs:      []string{"https://photos.example/arrival.jpg"},
			DispatchStatus: "technician_follow_up_complete",
		},
	}}}
	req := httptest.NewRequest(http.MethodPost, "/dead-letters/WO-1842/redrive", nil)
	res := httptest.NewRecorder()

	service{dlq: dlq, queue: fieldServiceQueue}.routes().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if len(dlq.redriven) != 1 || dlq.redriven[0] != "msg-17" {
		t.Fatalf("redriven = %v, want [msg-17]", dlq.redriven)
	}
}
