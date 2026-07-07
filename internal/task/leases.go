package task

import (
	"context"
	"fmt"
	"time"

	"midgard/internal/model"
	"midgard/internal/state"
)

type Lease struct {
	ID     string
	TaskID string
	Role   model.Role
	State  string
}

func grantLease(ctx context.Context, db *state.DB, taskID string, role model.Role) (Lease, error) {
	lease := Lease{
		ID:     fmt.Sprintf("lease_%s_%s_%d", taskID, role, time.Now().UTC().UnixNano()),
		TaskID: taskID,
		Role:   role,
		State:  "active",
	}
	if err := db.InsertLease(ctx, lease.ID, lease.TaskID, lease.Role.String(), lease.State); err != nil {
		return Lease{}, err
	}
	return lease, nil
}
