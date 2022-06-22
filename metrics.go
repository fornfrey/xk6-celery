package celery

import (
	"go.k6.io/k6/js/modules"
	"go.k6.io/k6/metrics"
)

type celeryMetrics struct {
	Tasks          *metrics.Metric
	TaskRuntime    *metrics.Metric
	TasksSucceeded *metrics.Metric
}

func registerMetrics(vu modules.VU) (*celeryMetrics, error) {
	var err error
	registry := vu.InitEnv().Registry
	m := celeryMetrics{}

	m.Tasks, err = registry.NewMetric("celery_tasks", metrics.Counter)
	if err != nil {
		return nil, err
	}

	m.TasksSucceeded, err = registry.NewMetric("celery_tasks_succeeded", metrics.Rate)
	if err != nil {
		return nil, err
	}

	m.TaskRuntime, err = registry.NewMetric("celery_task_runtime", metrics.Trend, metrics.Time)
	if err != nil {
		return nil, err
	}

	return &m, nil
}
