package celery

type taskEventMeta struct {
	Type      string  `json:"type"`
	TaskId    string  `json:"uuid"`
	Timestamp float64 `json:"timestamp"`
}

type taskSucceeded struct {
	taskEventMeta
	Runtime float64 `json:"runtime"`
}

type taskFailed struct {
	taskEventMeta
}
