// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClockSkew(t *testing.T) {
	now := metav1.Now()
	ahead := metav1.NewTime(now.Add(2 * time.Minute))
	behind := metav1.NewTime(now.Add(-2 * time.Minute))
	nearby := metav1.NewTime(now.Add(10 * time.Second))

	tests := []struct {
		name          string
		writeTime     *metav1.Time
		lastHeartbeat *metav1.Time
		wantExceeds   bool
	}{
		{name: "nil write time", writeTime: nil, lastHeartbeat: &now, wantExceeds: false},
		{name: "nil last heartbeat", writeTime: &now, lastHeartbeat: nil, wantExceeds: false},
		{name: "in sync", writeTime: &now, lastHeartbeat: &nearby, wantExceeds: false},
		{name: "host clock behind", writeTime: &now, lastHeartbeat: &behind, wantExceeds: true},
		{name: "host clock ahead", writeTime: &now, lastHeartbeat: &ahead, wantExceeds: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, exceeds := clockSkew(tt.writeTime, tt.lastHeartbeat)
			assert.Equal(t, tt.wantExceeds, exceeds)
		})
	}
}
