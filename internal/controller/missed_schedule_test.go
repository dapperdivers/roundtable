package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

func TestMissedSchedule(t *testing.T) {
	r := &ChainReconciler{}
	deadline := int64(300)

	chainWith := func(schedule string, lastScheduled *time.Time, phase aiv1alpha1.ChainPhase, deadlineSeconds *int64) *aiv1alpha1.Chain {
		c := &aiv1alpha1.Chain{
			Spec: aiv1alpha1.ChainSpec{
				Schedule:                schedule,
				StartingDeadlineSeconds: deadlineSeconds,
			},
			Status: aiv1alpha1.ChainStatus{Phase: phase},
		}
		if lastScheduled != nil {
			ts := metav1.NewTime(*lastScheduled)
			c.Status.LastScheduledAt = &ts
		}
		return c
	}

	hourAgo := time.Now().Add(-time.Hour)
	minuteAgo := time.Now().Add(-30 * time.Second)

	tests := []struct {
		name  string
		chain *aiv1alpha1.Chain
		want  bool
	}{
		{
			name:  "never scheduled before",
			chain: chainWith("*/5 * * * *", nil, aiv1alpha1.ChainPhaseIdle, nil),
			want:  false,
		},
		{
			name:  "missed fire with no deadline",
			chain: chainWith("*/5 * * * *", &hourAgo, aiv1alpha1.ChainPhaseSucceeded, nil),
			want:  true,
		},
		{
			name:  "missed fire but outside deadline",
			chain: chainWith("*/5 * * * *", &hourAgo, aiv1alpha1.ChainPhaseSucceeded, &deadline),
			want:  false,
		},
		{
			name:  "missed fire while still running",
			chain: chainWith("*/5 * * * *", &hourAgo, aiv1alpha1.ChainPhaseRunning, nil),
			want:  false,
		},
		{
			name:  "next fire not yet due",
			chain: chainWith("0 0 * * *", &minuteAgo, aiv1alpha1.ChainPhaseSucceeded, nil),
			want:  false,
		},
		{
			name:  "invalid schedule expression",
			chain: chainWith("not-a-cron", &hourAgo, aiv1alpha1.ChainPhaseSucceeded, nil),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.missedSchedule(tt.chain); got != tt.want {
				t.Errorf("missedSchedule() = %v, want %v", got, tt.want)
			}
		})
	}
}
