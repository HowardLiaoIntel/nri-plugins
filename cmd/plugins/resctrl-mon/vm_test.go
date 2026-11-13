// Copyright The NRI Plugins Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assignRecorder records every PID that AssignPID is asked to move.
type assignRecorder struct {
	tasksFileManager
	assigned []int
}

func (m *assignRecorder) AssignPID(key string, pid int) error {
	m.assigned = append(m.assigned, pid)
	return m.tasksFileManager.AssignPID(key, pid)
}

func fakeProc(t *testing.T, tgid int, tids ...int) string {
	t.Helper()
	root := t.TempDir()
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	for _, tid := range append([]int{tgid}, tids...) {
		addThread(t, tgid, tid)
	}
	return root
}

func addThread(t *testing.T, tgid, tid int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(procRoot, strconv.Itoa(tgid), "task", strconv.Itoa(tid)), 0o755))
}

func newRecordingNamespacePlugin(t *testing.T) (*plugin, *assignRecorder) {
	t.Helper()
	p := newNamespaceTestPlugin(t, t.TempDir())
	rec := &assignRecorder{tasksFileManager: p.mgr.(tasksFileManager)}
	p.mgr = rec
	return p, rec
}

func TestIsVMPod(t *testing.T) {
	pod := makePod("a1b2c3d4-e5f6-7890-abcd-ef1234567890", "prod", "a")
	assert.False(t, isVMPod(pod))
	pod.RuntimeHandler = "runc"
	assert.False(t, isVMPod(pod))
	for _, h := range []string{"kata-qemu", "kata", "Kata-Clh"} {
		pod.RuntimeHandler = h
		assert.True(t, isVMPod(pod), h)
	}
}

func TestVMPod_AllThreadsMoved(t *testing.T) {
	fakeProc(t, 500, 501, 502)
	p, rec := newRecordingNamespacePlugin(t)

	pod := makePod("a1b2c3d4-e5f6-7890-abcd-ef1234567890", "prod", "kata")
	pod.RuntimeHandler = "kata-qemu"
	startPod(t, p, pod, 500)

	assert.ElementsMatch(t, []int{500, 501, 502}, rec.assigned,
		"every QEMU thread must move, not only the main one")
}

func TestPlainPod_OnlyInitPIDMoved(t *testing.T) {
	fakeProc(t, 600, 601, 602)
	p, rec := newRecordingNamespacePlugin(t)

	pod := makePod("b1b2c3d4-e5f6-7890-abcd-ef1234567890", "prod", "plain")
	startPod(t, p, pod, 600)

	assert.Equal(t, []int{600}, rec.assigned, "children inherit the RMID, so one PID is enough")
}

func TestVMPod_ReconcilePicksUpNewThreadsAndForgetsExitedVM(t *testing.T) {
	fakeProc(t, 500, 501)
	p, rec := newRecordingNamespacePlugin(t)

	pod := makePod("a1b2c3d4-e5f6-7890-abcd-ef1234567890", "prod", "kata")
	pod.RuntimeHandler = "kata-qemu"
	startPod(t, p, pod, 500)
	rec.assigned = nil

	addThread(t, 500, 503)
	p.opMu.Lock()
	p.reconcile(p.mgr)
	p.opMu.Unlock()
	assert.Contains(t, rec.assigned, 503, "a thread created after the first pass must be picked up")
	assert.Contains(t, p.vmPIDs["ns-prod"], 500)

	require.NoError(t, os.RemoveAll(filepath.Join(procRoot, "500")))
	p.opMu.Lock()
	p.reconcile(p.mgr)
	p.opMu.Unlock()
	assert.Empty(t, p.vmPIDs, "an exited VM must be forgotten")
}

func TestVMPod_RemoveLastSandboxClearsTracking(t *testing.T) {
	fakeProc(t, 500, 501)
	p, _ := newRecordingNamespacePlugin(t)

	pod := makePod("a1b2c3d4-e5f6-7890-abcd-ef1234567890", "prod", "kata")
	pod.RuntimeHandler = "kata-qemu"
	startPod(t, p, pod, 500)
	require.NotEmpty(t, p.vmPIDs)

	require.NoError(t, os.Remove(filepath.Join(p.config.ResctrlPath, "mon_groups", "ns-prod", "tasks")))
	require.NoError(t, p.RemovePodSandbox(context.Background(), pod))
	assert.Empty(t, p.vmPIDs)
}
