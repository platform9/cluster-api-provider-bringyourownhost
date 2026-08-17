// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHeartbeatDue(t *testing.T) {
	interval := 10 * time.Second
	base := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	tests := []struct {
		name string
		last *metav1.Time
		now  metav1.Time
		want bool
	}{
		{"no prior heartbeat", nil, base, true},
		{"just under interval", &base, metav1.NewTime(base.Add(9 * time.Second)), false},
		{"exactly at interval", &base, metav1.NewTime(base.Add(10 * time.Second)), true},
		{"well past interval", &base, metav1.NewTime(base.Add(time.Minute)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, heartbeatDue(tt.last, tt.now, interval))
		})
	}
}

func TestBuildHeartbeatPatch(t *testing.T) {
	now := metav1.Now()

	t.Run("includes machineID on success", func(t *testing.T) {
		b, err := buildHeartbeatPatch(now, "v1.2.3", "abc-123", nil)
		require.NoError(t, err)

		var decoded struct {
			Status struct {
				MachineID string `json:"machineID"`
			} `json:"status"`
		}
		require.NoError(t, json.Unmarshal(b, &decoded))
		assert.Equal(t, "abc-123", decoded.Status.MachineID)
	})

	t.Run("omits machineID on read failure, instead of clobbering it with an empty value", func(t *testing.T) {
		b, err := buildHeartbeatPatch(now, "v1.2.3", "", errors.New("failed to read /etc/machine-id"))
		require.NoError(t, err)

		var decoded map[string]map[string]interface{}
		require.NoError(t, json.Unmarshal(b, &decoded))
		_, hasMachineID := decoded["status"]["machineID"]
		assert.False(t, hasMachineID, "machineID must be absent from the patch, not present with an empty value")
	})
}
