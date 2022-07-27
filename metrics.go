package celery

import (
	"go.k6.io/k6/js/modules"
	"go.k6.io/k6/metrics"
)

type celeryMetrics struct {
	Tasks          *metrics.Metric
	ChildTasks     *metrics.Metric
	TasksTotal     *metrics.Metric
	TasksSucceeded *metrics.Metric
	TasksRetried   *metrics.Metric

	TaskRuntime   *metrics.Metric
	TaskQueueTime *metrics.Metric
}

func registerMetrics(vu modules.VU) *celeryMetrics {
	registry := vu.InitEnv().Registry
	m := new(celeryMetrics)

	m.Tasks = registry.MustNewMetric("celery_tasks", metrics.Counter)
	m.ChildTasks = registry.MustNewMetric("celery_tasks_child", metrics.Counter)
	m.TasksTotal = registry.MustNewMetric("celery_tasks_total", metrics.Counter)
	m.TasksSucceeded = registry.MustNewMetric("celery_tasks_succeeded", metrics.Rate)
	m.TasksRetried = registry.MustNewMetric("celery_tasks_retried", metrics.Counter)
	m.TaskRuntime = registry.MustNewMetric("celery_task_runtime", metrics.Trend, metrics.Time)
	m.TaskQueueTime = registry.MustNewMetric("celery_task_queue_time", metrics.Trend, metrics.Time)

	return m
}
