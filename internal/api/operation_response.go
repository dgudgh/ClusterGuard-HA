package api

import (
	"time"

	"clusterguard.io/ha/pkg/model"
)

type executionResponse struct {
	model.Execution
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type operationRecordResponse struct {
	model.OperationRecord
	Execution executionResponse `json:"execution"`
}

func presentExecution(value model.Execution) executionResponse {
	result := executionResponse{Execution: value}
	if !value.StartedAt.IsZero() {
		result.StartedAt = &value.StartedAt
	}
	if !value.FinishedAt.IsZero() {
		result.FinishedAt = &value.FinishedAt
	}
	return result
}

// Project operation timestamps only at the HTTP boundary. Model JSON encoding
// remains unchanged because it is also used for persisted snapshot digests.
func presentOperationResponse(value interface{}) interface{} {
	switch value := value.(type) {
	case model.Execution:
		return presentExecution(value)
	case *model.Execution:
		if value == nil {
			return nil
		}
		return presentExecution(*value)
	case model.OperationRecord:
		return operationRecordResponse{OperationRecord: value, Execution: presentExecution(value.Execution)}
	case *model.OperationRecord:
		if value == nil {
			return nil
		}
		return presentOperationResponse(*value)
	case []model.OperationRecord:
		if value == nil {
			return value
		}
		result := make([]operationRecordResponse, len(value))
		for index, record := range value {
			result[index] = operationRecordResponse{OperationRecord: record, Execution: presentExecution(record.Execution)}
		}
		return result
	case map[string]interface{}:
		if value == nil {
			return value
		}
		result := make(map[string]interface{}, len(value))
		for key, item := range value {
			result[key] = presentOperationResponse(item)
		}
		return result
	case []interface{}:
		if value == nil {
			return value
		}
		result := make([]interface{}, len(value))
		for index, item := range value {
			result[index] = presentOperationResponse(item)
		}
		return result
	default:
		return value
	}
}
