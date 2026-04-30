package domain

import "time"

type Task struct {
	ID        string
	ThreadID  string
	Intent    Intent
	Status    TaskStatus
	Input     string
	Result    string
	WorkerID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
)

type Intent string

const (
	IntentCodeTask   Intent = "code_task"
	IntentQuestion   Intent = "question"
	IntentDiscussion Intent = "discussion"
	IntentReview     Intent = "review"
	IntentUnknown    Intent = "unknown"
)
