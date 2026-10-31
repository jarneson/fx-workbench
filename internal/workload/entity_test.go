package workload

import "testing"

func TestTaskReviewLifecycleStatuses(t *testing.T) {
	for _, status := range []string{"coding", "self-review", "in-review"} {
		if !ValidTaskStatus(status) || !ValidStatus(status) {
			t.Errorf("%s should be a valid task status", status)
		}
	}
}

func TestMergedStatusAndDependencySatisfaction(t *testing.T) {
	if !ValidStatus("merged") {
		t.Fatal("merged should be a valid status")
	}
	if !ValidStatus("closed") {
		t.Fatal("closed should be a valid project/workstream status")
	}
	if DependencySatisfied("done") {
		t.Fatal("done task should not satisfy dependencies before merge")
	}
	if !DependencySatisfied("merged") {
		t.Fatal("merged task should satisfy dependencies")
	}
}
