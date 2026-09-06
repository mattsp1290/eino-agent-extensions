package delegatetask

import (
	"encoding/json"
	"testing"
)

func TestResponseAndResultStatusContracts(t *testing.T) {
	responses := []ResponseStatus{ResponseCompleted, ResponseFailed, ResponseRejected, ResponseUnavailable}
	wantResponses := []string{"completed", "failed", "rejected", "unavailable"}
	for i := range responses {
		if string(responses[i]) != wantResponses[i] {
			t.Fatalf("response status %d=%q", i, responses[i])
		}
	}
	statuses := []Status{StatusCompleted, StatusFailed, StatusRejected, StatusUnavailable, StatusTimedOut}
	wantStatuses := []string{"completed", "failed", "rejected", "unavailable", "timed_out"}
	for i := range statuses {
		if string(statuses[i]) != wantStatuses[i] {
			t.Fatalf("status %d=%q", i, statuses[i])
		}
	}
}

func TestResultCanonicalJSON(t *testing.T) {
	for _, test := range []struct {
		result Result
		want   string
	}{
		{Result{Status: StatusCompleted}, `{"status":"completed"}`},
		{Result{Status: StatusFailed, Output: "details\n"}, `{"status":"failed","output":"details\n"}`},
	} {
		got, err := json.Marshal(test.result)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.want {
			t.Fatalf("got=%s want=%s", got, test.want)
		}
	}
}
