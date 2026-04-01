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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// KnightsTotal tracks the total number of knights by phase and table.
	// Labels: phase (Ready, Provisioning, Degraded, Suspended), table (roundtable name)
	KnightsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "roundtable_knights_total",
			Help: "Total number of knights by phase and table",
		},
		[]string{"phase", "table"},
	)

	// TasksCompletedTotal tracks cumulative tasks completed by knights.
	// Labels: knight (knight name), domain (knight domain), table (roundtable name)
	TasksCompletedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "roundtable_tasks_completed_total",
			Help: "Total tasks completed by knights",
		},
		[]string{"knight", "domain", "table"},
	)

	// TaskDurationSeconds tracks task execution duration as a histogram.
	// Labels: knight (knight name), domain (knight domain)
	// Buckets: 1s, 2s, 4s, 8s, 16s, 32s, 64s, 128s, 256s, 512s (~17min)
	TaskDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "roundtable_task_duration_seconds",
			Help:    "Task execution duration in seconds",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s to ~17min
		},
		[]string{"knight", "domain"},
	)

	// ChainRunsTotal tracks total chain runs by status.
	// Labels: chain (chain name), status (succeeded, failed)
	ChainRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "roundtable_chain_runs_total",
			Help: "Total chain runs by status",
		},
		[]string{"chain", "status"}, // status: succeeded, failed
	)

	// MissionsTotal tracks the total number of missions by phase.
	// Labels: phase (Pending, Provisioning, Planning, Assembling, Active, Succeeded, Failed, etc.)
	MissionsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "roundtable_missions_total",
			Help: "Total missions by phase",
		},
		[]string{"phase"},
	)

	// CostTotalUSD tracks cumulative cost in USD by table.
	// Labels: table (roundtable name)
	CostTotalUSD = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "roundtable_cost_total_usd",
			Help: "Total cost in USD by table",
		},
		[]string{"table"},
	)

	// WarmPoolSize tracks the number of warm pool knights by state.
	// Labels: state (available, provisioning, claimed), table (roundtable name)
	WarmPoolSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "roundtable_warm_pool_size",
			Help: "Warm pool knights by state",
		},
		[]string{"state", "table"}, // state: available, provisioning, claimed
	)

	// ReconcileErrorsTotal tracks reconciliation errors by controller.
	// Labels: controller (knight-controller, chain-controller, mission-controller, roundtable-controller)
	ReconcileErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "roundtable_reconcile_errors_total",
			Help: "Total reconciliation errors by controller",
		},
		[]string{"controller"},
	)
)

func init() {
	// Register all custom metrics with the controller-runtime metrics registry
	metrics.Registry.MustRegister(
		KnightsTotal,
		TasksCompletedTotal,
		TaskDurationSeconds,
		ChainRunsTotal,
		MissionsTotal,
		CostTotalUSD,
		WarmPoolSize,
		ReconcileErrorsTotal,
	)
}
