package gha

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type capturedStep struct {
	ID          string
	Name        string
	Status      string
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
}

type capturedJob struct {
	Key         string
	ID          string
	Name        string
	Matrix      map[string]interface{}
	StartedAt   time.Time
	CompletedAt time.Time
	Status      string
	Conclusion  string
	StepOrder   []string
	Steps       map[string]*capturedStep
	LogLines    []string
}

type actLogCapture struct {
	jobs         map[string]*capturedJob
	jobKeys      map[string]string
	combinedLogs []string
}

func newActLogCapture() *actLogCapture {
	return &actLogCapture{
		jobs:    make(map[string]*capturedJob),
		jobKeys: make(map[string]string),
	}
}

func (c *actLogCapture) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (c *actLogCapture) Fire(entry *logrus.Entry) error {
	job := c.ensureJob(entry)
	if job == nil {
		return nil
	}

	if job.StartedAt.IsZero() {
		job.StartedAt = entry.Time
	}

	if raw, _ := entry.Data["raw_output"].(bool); raw {
		line := strings.TrimRight(entry.Message, "\r\n")
		job.LogLines = append(job.LogLines, line)
		c.combinedLogs = append(c.combinedLogs, fmt.Sprintf("[%s] %s", job.Key, line))
	}

	stage, _ := entry.Data["stage"].(string)
	step := c.ensureStep(job, entry)
	if stage == "Main" && step != nil && step.StartedAt.IsZero() && strings.HasPrefix(entry.Message, "⭐ Run Main ") {
		step.StartedAt = entry.Time
	}

	if stepResult, ok := entry.Data["stepResult"]; ok && step != nil {
		status := normalizeLogStatus(stepResult)
		if status != "" {
			step.Status = status
			step.Conclusion = status
			if step.StartedAt.IsZero() {
				step.StartedAt = entry.Time
			}
			step.CompletedAt = entry.Time
		}
	}

	if jobResult, ok := entry.Data["jobResult"]; ok {
		status := normalizeLogStatus(jobResult)
		if status != "" {
			job.Status = status
			job.Conclusion = status
			job.CompletedAt = entry.Time
		}
	}

	return nil
}

func (c *actLogCapture) ensureJob(entry *logrus.Entry) *capturedJob {
	jobID, _ := entry.Data["jobID"].(string)
	if jobID == "" {
		return nil
	}
	jobName, _ := entry.Data["job"].(string)
	matrix := cloneMatrix(entry.Data["matrix"])
	key := uniqueJobKey(jobID, matrix)
	if existing, ok := c.jobs[key]; ok {
		return existing
	}
	c.jobKeys[jobID] = key
	job := &capturedJob{
		Key:    key,
		ID:     jobID,
		Name:   jobName,
		Matrix: matrix,
		Steps:  make(map[string]*capturedStep),
	}
	c.jobs[key] = job
	return job
}

func (c *actLogCapture) ensureStep(job *capturedJob, entry *logrus.Entry) *capturedStep {
	stepID := lastStepID(entry.Data["stepID"])
	if stepID == "" {
		return nil
	}
	if existing, ok := job.Steps[stepID]; ok {
		if existing.Name == "" {
			if name, _ := entry.Data["step"].(string); name != "" {
				existing.Name = name
			}
		}
		return existing
	}
	stepName, _ := entry.Data["step"].(string)
	step := &capturedStep{
		ID:   stepID,
		Name: stepName,
	}
	job.Steps[stepID] = step
	job.StepOrder = append(job.StepOrder, stepID)
	return step
}

func (c *actLogCapture) snapshot() (map[string]WorkflowJobOutput, []string, map[string][]string) {
	if len(c.jobs) == 0 {
		return nil, c.combinedLogs, nil
	}

	jobKeys := make([]string, 0, len(c.jobs))
	for key := range c.jobs {
		jobKeys = append(jobKeys, key)
	}
	sort.Strings(jobKeys)

	jobs := make(map[string]WorkflowJobOutput, len(jobKeys))
	perJobLogs := make(map[string][]string, len(jobKeys))
	for _, key := range jobKeys {
		job := c.jobs[key]
		steps := make([]WorkflowStepOutput, 0, len(job.StepOrder))
		for _, stepID := range job.StepOrder {
			step := job.Steps[stepID]
			if step == nil {
				continue
			}
			steps = append(steps, WorkflowStepOutput{
				Name:            step.Name,
				Status:          fallbackStatus(step.Status, statusSuccess),
				Conclusion:      fallbackStatus(step.Conclusion, step.Status),
				DurationSeconds: durationSeconds(step.StartedAt, step.CompletedAt),
			})
		}
		jobs[key] = WorkflowJobOutput{
			Name:            job.Name,
			Status:          fallbackStatus(job.Status, statusSuccess),
			Conclusion:      fallbackStatus(job.Conclusion, job.Status),
			DurationSeconds: durationSeconds(job.StartedAt, job.CompletedAt),
			Matrix:          cloneMatrix(job.Matrix),
			Steps:           steps,
		}
		perJobLogs[key] = append([]string(nil), job.LogLines...)
	}

	return jobs, append([]string(nil), c.combinedLogs...), perJobLogs
}

func uniqueJobKey(jobID string, matrix map[string]interface{}) string {
	if len(matrix) == 0 {
		return jobID
	}
	keys := make([]string, 0, len(matrix))
	for key := range matrix {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, matrix[key]))
	}
	return fmt.Sprintf("%s[%s]", jobID, strings.Join(parts, ","))
}

func cloneMatrix(raw interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	src, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func lastStepID(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []string:
		if len(value) == 0 {
			return ""
		}
		return value[len(value)-1]
	case []interface{}:
		if len(value) == 0 {
			return ""
		}
		last, _ := value[len(value)-1].(string)
		return last
	default:
		return ""
	}
}

func normalizeLogStatus(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case interface{ String() string }:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func durationSeconds(start, end time.Time) int {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Seconds())
}

func fallbackStatus(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func deriveWorkflowStatus(jobs map[string]WorkflowJobOutput, timedOut bool) (string, int) {
	if timedOut {
		return statusTimedOut, 1
	}
	if len(jobs) == 0 {
		return statusSuccess, 0
	}
	anyCancelled := false
	for _, job := range jobs {
		switch job.Status {
		case statusFailure:
			return statusFailure, 1
		case statusCancelled:
			anyCancelled = true
		}
	}
	if anyCancelled {
		return statusCancelled, 1
	}
	return statusSuccess, 0
}
