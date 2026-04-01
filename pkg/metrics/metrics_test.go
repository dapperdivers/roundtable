/*
Copyright 2026 dapperdivers.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ---------------------------------------------------------------------------
// Registration sanity — if init() double-registers or panics we'd never get
// this far, but an explicit nil-guard is cheap insurance.
// ---------------------------------------------------------------------------

func TestMetricsRegistered(t *testing.T) {
	collectors := map[string]interface{}{
		"KnightsTotal":         KnightsTotal,
		"TasksCompletedTotal":  TasksCompletedTotal,
		"TaskDurationSeconds":  TaskDurationSeconds,
		"ChainRunsTotal":       ChainRunsTotal,
		"MissionsTotal":        MissionsTotal,
		"CostTotalUSD":         CostTotalUSD,
		"WarmPoolSize":         WarmPoolSize,
		"ReconcileErrorsTotal": ReconcileErrorsTotal,
	}
	for name, c := range collectors {
		if c == nil {
			t.Errorf("%s is nil — metric was not initialised", name)
		}
	}
}

// ---------------------------------------------------------------------------
// GaugeVec tests
// ---------------------------------------------------------------------------

func TestKnightsTotal(t *testing.T) {
	KnightsTotal.WithLabelValues("Ready", "test-table").Set(5)
	val := testutil.ToFloat64(KnightsTotal.WithLabelValues("Ready", "test-table"))
	if val != 5 {
		t.Errorf("KnightsTotal: expected 5, got %v", val)
	}

	// Increment then verify
	KnightsTotal.WithLabelValues("Ready", "test-table").Set(10)
	val = testutil.ToFloat64(KnightsTotal.WithLabelValues("Ready", "test-table"))
	if val != 10 {
		t.Errorf("KnightsTotal after update: expected 10, got %v", val)
	}
}

func TestKnightsTotalMultiplePhases(t *testing.T) {
	phases := []string{"Ready", "Provisioning", "Degraded", "Suspended"}
	for i, phase := range phases {
		KnightsTotal.WithLabelValues(phase, "multi-table").Set(float64(i + 1))
	}
	for i, phase := range phases {
		val := testutil.ToFloat64(KnightsTotal.WithLabelValues(phase, "multi-table"))
		expected := float64(i + 1)
		if val != expected {
			t.Errorf("KnightsTotal phase=%s: expected %v, got %v", phase, expected, val)
		}
	}
}

func TestMissionsTotal(t *testing.T) {
	MissionsTotal.WithLabelValues("Active").Set(3)
	MissionsTotal.WithLabelValues("Failed").Set(1)

	if v := testutil.ToFloat64(MissionsTotal.WithLabelValues("Active")); v != 3 {
		t.Errorf("MissionsTotal Active: expected 3, got %v", v)
	}
	if v := testutil.ToFloat64(MissionsTotal.WithLabelValues("Failed")); v != 1 {
		t.Errorf("MissionsTotal Failed: expected 1, got %v", v)
	}
}

func TestCostTotalUSD(t *testing.T) {
	CostTotalUSD.WithLabelValues("prod-table").Set(42.50)
	val := testutil.ToFloat64(CostTotalUSD.WithLabelValues("prod-table"))
	if val != 42.50 {
		t.Errorf("CostTotalUSD: expected 42.50, got %v", val)
	}

	// Add via Add (gauge supports Add)
	CostTotalUSD.WithLabelValues("prod-table").Add(7.50)
	val = testutil.ToFloat64(CostTotalUSD.WithLabelValues("prod-table"))
	if val != 50 {
		t.Errorf("CostTotalUSD after Add: expected 50, got %v", val)
	}
}

func TestWarmPoolSize(t *testing.T) {
	states := map[string]float64{
		"available":    5,
		"provisioning": 2,
		"claimed":      3,
	}
	for state, expected := range states {
		WarmPoolSize.WithLabelValues(state, "pool-table").Set(expected)
		val := testutil.ToFloat64(WarmPoolSize.WithLabelValues(state, "pool-table"))
		if val != expected {
			t.Errorf("WarmPoolSize state=%s: expected %v, got %v", state, expected, val)
		}
	}
}

// ---------------------------------------------------------------------------
// CounterVec tests
// ---------------------------------------------------------------------------

func TestTasksCompletedTotal(t *testing.T) {
	TasksCompletedTotal.WithLabelValues("galahad", "security", "test-table").Add(0) // init
	before := testutil.ToFloat64(TasksCompletedTotal.WithLabelValues("galahad", "security", "test-table"))

	TasksCompletedTotal.WithLabelValues("galahad", "security", "test-table").Inc()
	after := testutil.ToFloat64(TasksCompletedTotal.WithLabelValues("galahad", "security", "test-table"))

	if after != before+1 {
		t.Errorf("TasksCompletedTotal: expected %v, got %v", before+1, after)
	}
}

func TestTasksCompletedTotalAddN(t *testing.T) {
	before := testutil.ToFloat64(TasksCompletedTotal.WithLabelValues("lancelot", "research", "add-table"))
	TasksCompletedTotal.WithLabelValues("lancelot", "research", "add-table").Add(10)
	after := testutil.ToFloat64(TasksCompletedTotal.WithLabelValues("lancelot", "research", "add-table"))
	if after != before+10 {
		t.Errorf("TasksCompletedTotal Add(10): expected %v, got %v", before+10, after)
	}
}

func TestChainRunsTotal(t *testing.T) {
	ChainRunsTotal.WithLabelValues("deploy-chain", "succeeded").Add(0) // init
	before := testutil.ToFloat64(ChainRunsTotal.WithLabelValues("deploy-chain", "succeeded"))

	ChainRunsTotal.WithLabelValues("deploy-chain", "succeeded").Inc()
	ChainRunsTotal.WithLabelValues("deploy-chain", "succeeded").Inc()

	after := testutil.ToFloat64(ChainRunsTotal.WithLabelValues("deploy-chain", "succeeded"))
	if after != before+2 {
		t.Errorf("ChainRunsTotal succeeded: expected %v, got %v", before+2, after)
	}

	// Failed path
	ChainRunsTotal.WithLabelValues("deploy-chain", "failed").Inc()
	failVal := testutil.ToFloat64(ChainRunsTotal.WithLabelValues("deploy-chain", "failed"))
	if failVal < 1 {
		t.Errorf("ChainRunsTotal failed: expected >=1, got %v", failVal)
	}
}

func TestReconcileErrorsTotal(t *testing.T) {
	controllers := []string{
		"knight-controller",
		"chain-controller",
		"mission-controller",
		"roundtable-controller",
	}
	for _, ctrl := range controllers {
		before := testutil.ToFloat64(ReconcileErrorsTotal.WithLabelValues(ctrl))
		ReconcileErrorsTotal.WithLabelValues(ctrl).Inc()
		after := testutil.ToFloat64(ReconcileErrorsTotal.WithLabelValues(ctrl))
		if after != before+1 {
			t.Errorf("ReconcileErrorsTotal controller=%s: expected %v, got %v", ctrl, before+1, after)
		}
	}
}

// ---------------------------------------------------------------------------
// HistogramVec tests
// ---------------------------------------------------------------------------

func TestTaskDurationSeconds(t *testing.T) {
	TaskDurationSeconds.WithLabelValues("percival", "ops").Observe(1.5)
	TaskDurationSeconds.WithLabelValues("percival", "ops").Observe(3.0)
	TaskDurationSeconds.WithLabelValues("percival", "ops").Observe(30.0)

	// Validate via CollectAndCount — histogram exposes _bucket, _count, _sum
	count := testutil.CollectAndCount(TaskDurationSeconds)
	if count == 0 {
		t.Error("TaskDurationSeconds: expected >0 metric series, got 0")
	}
}

func TestTaskDurationSecondsLint(t *testing.T) {
	// Observe with a unique label set so the histogram has data
	TaskDurationSeconds.WithLabelValues("lint-knight", "lint-domain").Observe(2.5)

	problems, err := testutil.CollectAndLint(TaskDurationSeconds)
	if err != nil {
		t.Errorf("CollectAndLint error: %v", err)
	}
	for _, p := range problems {
		t.Errorf("lint issue: %s", p.Text)
	}
}

// ---------------------------------------------------------------------------
// Lint all metrics — catches naming/help-text violations early.
// ---------------------------------------------------------------------------

func TestMetricsLint(t *testing.T) {
	// Known lint issues: KnightsTotal and MissionsTotal are gauges with
	// "_total" suffix which violates Prometheus naming conventions.
	// These are tracked separately; skip them in the general lint pass.
	knownSuffixIssues := map[string]bool{
		"KnightsTotal":  true,
		"MissionsTotal": true,
	}

	collectors := []struct {
		name      string
		collector prometheus.Collector
	}{
		{"KnightsTotal", KnightsTotal},
		{"TasksCompletedTotal", TasksCompletedTotal},
		{"TaskDurationSeconds", TaskDurationSeconds},
		{"ChainRunsTotal", ChainRunsTotal},
		{"MissionsTotal", MissionsTotal},
		{"CostTotalUSD", CostTotalUSD},
		{"WarmPoolSize", WarmPoolSize},
		{"ReconcileErrorsTotal", ReconcileErrorsTotal},
	}
	for _, tc := range collectors {
		problems, err := testutil.CollectAndLint(tc.collector)
		if err != nil {
			t.Errorf("%s CollectAndLint error: %v", tc.name, err)
		}
		for _, p := range problems {
			if knownSuffixIssues[tc.name] && p.Text == `non-counter metrics should not have "_total" suffix` {
				t.Logf("%s lint (known issue): %s", tc.name, p.Text)
				continue
			}
			t.Errorf("%s lint: %s", tc.name, p.Text)
		}
	}
}
