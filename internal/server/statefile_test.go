package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAndRemoveStateFile(t *testing.T) {
	dir := t.TempDir()

	path, err := WriteStateFile(dir, "127.0.0.1", 8080, "1.0.0", false)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, "server.8080.json"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var sf StateFile
	require.NoError(t, json.Unmarshal(data, &sf))
	assert.Equal(t, 8080, sf.Port)
	assert.Equal(t, "127.0.0.1", sf.Host)
	assert.Equal(t, "1.0.0", sf.Version)
	assert.Equal(t, os.Getpid(), sf.PID)
	assert.NotEmpty(t, sf.StartedAt)

	RemoveStateFile(dir, 8080)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "state file not removed")
}

// TestWriteStateFile_UsesProcessStartTime verifies that
// WriteStateFile records the actual process creation time,
// not the wall clock at write time. This ensures that a
// slow startup (sync > 120s) doesn't cause the state file
// to be misclassified as stale by processStartTime checks.
func TestWriteStateFile_UsesProcessStartTime(t *testing.T) {
	procStart, err := processStartTime(os.Getpid())
	if err != nil {
		t.Skipf("processStartTime not available: %v", err)
	}

	dir := t.TempDir()
	path, err := WriteStateFile(
		dir, "127.0.0.1", 7777, "1.0.0", false,
	)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf StateFile
	require.NoError(t, json.Unmarshal(data, &sf))

	started, err := time.Parse(time.RFC3339, sf.StartedAt)
	require.NoError(t, err)

	// StartedAt should match the process start time, not
	// time.Now(). With RFC3339Nano precision there is no
	// truncation, so we can use a tight tolerance (1ms
	// for platform rounding). We also verify StartedAt is
	// closer to procStart than to time.Now().
	now := time.Now()
	diffFromStart := started.Sub(procStart)
	if diffFromStart < 0 {
		diffFromStart = -diffFromStart
	}
	diffFromNow := started.Sub(now)
	if diffFromNow < 0 {
		diffFromNow = -diffFromNow
	}
	assert.LessOrEqual(t, diffFromStart, time.Millisecond,
		"StartedAt = %v, want ≈ process start %v", started, procStart)
	assert.GreaterOrEqual(t, diffFromNow, diffFromStart,
		"StartedAt %v is closer to Now %v than to process start %v",
		started, now, procStart)

	// The state file must pass hasLiveStateFile validation.
	assert.True(t, IsServerActive(dir),
		"state file written by WriteStateFile failed IsServerActive")
}

func TestFindRunningServer_NoFiles(t *testing.T) {
	dir := t.TempDir()
	assert.Nil(t, FindRunningServer(dir))
}

func TestFindRunningServer_StaleFile(t *testing.T) {
	dir := t.TempDir()

	// Write a state file with a PID that doesn't exist.
	sf := StateFile{
		PID:       999999999,
		Port:      9999,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.9999.json")
	os.WriteFile(path, data, 0o644)

	assert.Nil(t, FindRunningServer(dir), "expected nil for stale PID")

	// Stale file should be cleaned up.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "stale state file not cleaned up")
}

func TestFindRunningServer_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.8080.json")
	os.WriteFile(path, []byte("not json"), 0o644)

	assert.Nil(t, FindRunningServer(dir), "expected nil for invalid JSON")
}

func TestFindRunningServer_IgnoresNonStateFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(
		filepath.Join(dir, "config.json"),
		[]byte("{}"), 0o644,
	)
	os.WriteFile(
		filepath.Join(dir, "server.txt"),
		[]byte("nope"), 0o644,
	)

	assert.Nil(t, FindRunningServer(dir))
}

func TestFindRunningServer_LiveProcess(t *testing.T) {
	dir := t.TempDir()

	// Start a real TCP listener so the port probe succeeds.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      port,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(
		dir, fmt.Sprintf("server.%d.json", port),
	)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	result := FindRunningServer(dir)
	require.NotNil(t, result, "expected running server")
	assert.Equal(t, port, result.Port)
	assert.Equal(t, os.Getpid(), result.PID)
}

func TestFindRunningServer_BindAll(t *testing.T) {
	dir := t.TempDir()

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      port,
		Host:      "0.0.0.0",
		Version:   "1.0.0",
		StartedAt: "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(
		dir, fmt.Sprintf("server.%d.json", port),
	)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	result := FindRunningServer(dir)
	require.NotNil(t, result, "expected running server for 0.0.0.0 host")
	assert.Equal(t, port, result.Port)
}

// recentStartedAt returns a StartedAt timestamp that passes
// both the boot-time and process-start-time checks in
// hasLiveStateFile. When processStartTime is available, we
// use the actual start time of the test process; otherwise
// we fall back to a time that is after boot.
func recentStartedAt() string {
	if st, err := processStartTime(os.Getpid()); err == nil {
		return st.UTC().Format(time.RFC3339Nano)
	}
	started := time.Now().Add(-1 * time.Hour)
	if bt, err := systemBootTime(); err == nil {
		if started.Before(bt) {
			started = bt.Add(time.Second)
		}
	}
	return started.UTC().Format(time.RFC3339Nano)
}

// TestIsServerActive_LivePIDNoPort verifies that IsServerActive
// returns true when a state file has a live PID but no listening
// port (e.g., transient TCP probe failure or server under load).
func TestIsServerActive_LivePIDNoPort(t *testing.T) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59999,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59999.json")
	os.WriteFile(path, data, 0o644)

	// FindRunningServer should return nil (no TCP).
	assert.Nil(t, FindRunningServer(dir), "expected FindRunningServer nil (no listener)")

	// But IsServerActive should return true (live PID).
	assert.True(t, IsServerActive(dir), "expected IsServerActive true for live PID")

	// State file should NOT be deleted.
	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "state file was deleted despite live PID")
}

// TestIsServerActive_LivePIDNoPort_NoStartupLock verifies
// the exact scenario where a server is running but the TCP
// probe is transiently failing: IsServerActive is true,
// FindRunningServer is nil, but IsStartupLocked is false.
// token-use should NOT enter the wait path or fall back to
// on-demand sync in this case.
func TestIsServerActive_LivePIDNoPort_NoStartupLock(
	t *testing.T,
) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59998,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	os.WriteFile(
		filepath.Join(dir, "server.59998.json"), data, 0o644,
	)

	assert.Nil(t, FindRunningServer(dir), "expected FindRunningServer nil")
	assert.True(t, IsServerActive(dir), "expected IsServerActive true")
	assert.False(t, IsStartupLocked(dir), "expected IsStartupLocked false")
}

// TestIsServerActive_LongRunningServer verifies that a
// state file with a live PID is detected as active even
// when the TCP probe transiently fails. Uses the actual
// process start time to pass both boot-time and
// process-start-time validation layers.
func TestIsServerActive_LongRunningServer(t *testing.T) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59997,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59997.json")
	os.WriteFile(path, data, 0o644)

	assert.True(t, IsServerActive(dir), "expected IsServerActive true for old but live PID")

	// State file must NOT be deleted.
	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "state file was deleted for long-running server")
}

// TestIsServerActive_PreBootStateFile verifies that a state
// file from before the last system boot is treated as stale
// even if the PID is alive (PID reuse after reboot).
func TestIsServerActive_PreBootStateFile(t *testing.T) {
	dir := t.TempDir()

	bootTime, err := systemBootTime()
	if err != nil {
		t.Skipf("boot time not available: %v", err)
	}

	// State file from well before boot — simulates a crash
	// followed by a reboot where the PID was reused.
	preBootTime := bootTime.Add(-24 * time.Hour)
	sf := StateFile{
		PID:       os.Getpid(),
		Port:      59996,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: preBootTime.UTC().Format(time.RFC3339Nano),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59996.json")
	os.WriteFile(path, data, 0o644)

	assert.False(t, IsServerActive(dir),
		"expected false for pre-boot state file (PID reuse after reboot)")

	// Stale file should be cleaned up.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "pre-boot state file was not cleaned up")
}

// TestIsServerActive_DeadPIDStateFile verifies that a state
// file left behind after a server crash (dead PID) is cleaned
// up by hasLiveStateFile so IsServerActive returns false.
func TestIsServerActive_DeadPIDStateFile(t *testing.T) {
	dir := t.TempDir()

	sf := StateFile{
		PID:       999999999,
		Port:      59994,
		Host:      "127.0.0.1",
		Version:   "1.0.0",
		StartedAt: recentStartedAt(),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59994.json")
	os.WriteFile(path, data, 0o644)

	assert.False(t, IsServerActive(dir), "expected false for dead PID state file")

	// Dead-PID state file should be cleaned up.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "dead-PID state file not cleaned up")
}

// TestIsServerActive_StartupLock verifies that IsServerActive
// returns true when only the startup lock exists.
func TestIsServerActive_StartupLock(t *testing.T) {
	dir := t.TempDir()

	require.False(t, IsServerActive(dir), "expected false with no files")

	WriteStartupLock(dir)
	require.True(t, IsServerActive(dir), "expected true with startup lock")

	RemoveStartupLock(dir)
	require.False(t, IsServerActive(dir), "expected false after lock removed")
}

func TestStartupLock_OwnProcess(t *testing.T) {
	dir := t.TempDir()

	require.False(t, isServerStarting(dir), "expected false before lock written")

	WriteStartupLock(dir)
	require.True(t, isServerStarting(dir), "expected true after lock written")

	RemoveStartupLock(dir)
	require.False(t, isServerStarting(dir), "expected false after lock removed")
}

func TestStartupLock_StalePID(t *testing.T) {
	dir := t.TempDir()

	// Write a lock file with a PID that doesn't exist.
	path := filepath.Join(dir, startupLockFile(999999999))
	os.WriteFile(path, []byte("999999999"), 0o644)

	require.False(t, isServerStarting(dir), "expected false for stale PID")

	// Stale lock should be cleaned up.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "stale startup lock not cleaned up")
}

// TestStartupLock_MalformedContent verifies that a malformed
// lock file (e.g., partial write) is not deleted, since it
// could be a concurrent WriteStartupLock in progress.
func TestStartupLock_MalformedContent(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, startupLockPrefix+"bad")
	os.WriteFile(path, []byte("not-a-pid"), 0o644)

	require.False(t, isServerStarting(dir), "expected false for malformed content")

	// File should NOT be deleted.
	_, statErr := os.Stat(path)
	assert.False(t, os.IsNotExist(statErr), "malformed lock file was deleted")
}

// TestStartupLock_AtomicWrite verifies the lock file is written
// with content intact (no empty/partial file observable).
func TestStartupLock_AtomicWrite(t *testing.T) {
	dir := t.TempDir()

	WriteStartupLock(dir)

	path := filepath.Join(dir, startupLockFile(os.Getpid()))
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, fmt.Sprintf("%d", os.Getpid()), string(data))

	// No temp file should remain.
	tmpPath := path + ".tmp"
	_, tmpErr := os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(tmpErr), "temp file was not cleaned up")
}

func TestWaitForStartup_AlreadyRunning(t *testing.T) {
	dir := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	WriteStateFile(dir, "127.0.0.1", port, "1.0.0", false)

	// Should return immediately since server is running.
	assert.True(t, WaitForStartup(dir, 100*millisecondsForTest),
		"expected true, server is running")
}

func TestWaitForStartup_LockClearsNoServer(t *testing.T) {
	dir := t.TempDir()

	// No lock, no server — should return false immediately.
	assert.False(t, WaitForStartup(dir, 100*millisecondsForTest),
		"expected false, no lock and no server")
}

// millisecondsForTest is a scaling factor for test timeouts.
const millisecondsForTest = 1_000_000 // 1ms in ns

func TestProbeHostForDial(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"", "127.0.0.1"},
		{"0.0.0.0", "127.0.0.1"},
		{"::", "::1"},
		{"127.0.0.1", "127.0.0.1"},
		{"192.168.1.100", "192.168.1.100"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, probeHostForDial(tt.host),
			"probeHostForDial(%q)", tt.host)
	}
}

// TestProcessStartTime_OwnProcess verifies that
// processStartTime returns a reasonable time for the
// current process.
func TestProcessStartTime_OwnProcess(t *testing.T) {
	st, err := processStartTime(os.Getpid())
	if err != nil {
		t.Skipf("processStartTime not available: %v", err)
	}
	// Our process started before now.
	assert.False(t, st.After(time.Now()),
		"process start time %v is in the future", st)
	// And after boot (if available).
	if bt, btErr := systemBootTime(); btErr == nil {
		assert.False(t, st.Before(bt),
			"start %v is before boot %v", st, bt)
	}
}

// TestIsServerActive_SameBootPIDReuse verifies that a state
// file from the current boot whose PID is alive but belongs
// to a different process (same-boot PID reuse) is detected
// as stale and cleaned up. We simulate this by writing a
// state file with our own PID but a StartedAt far from our
// actual process start time.
func TestIsServerActive_SameBootPIDReuse(t *testing.T) {
	procStart, err := processStartTime(os.Getpid())
	if err != nil {
		t.Skipf("processStartTime not available: %v", err)
	}

	dir := t.TempDir()

	// StartedAt is 1 hour before our actual start time,
	// but after boot. This simulates another server that
	// wrote this state file, then crashed, and our PID
	// was reused.
	fakeStarted := procStart.Add(-1 * time.Hour)
	bt, btErr := systemBootTime()
	if btErr == nil && fakeStarted.Before(bt) {
		// If that would be pre-boot, place it just after
		// boot but still well before our start time.
		fakeStarted = bt.Add(time.Second)
	}
	// If fakeStarted is within tolerance of procStart,
	// we can't reliably test this case.
	diff := procStart.Sub(fakeStarted)
	if diff < 0 {
		diff = -diff
	}
	if diff <= stateFileStartTolerance {
		t.Skip("cannot simulate PID reuse: " +
			"fake start too close to actual")
	}

	sf := StateFile{
		PID:     os.Getpid(),
		Port:    59995,
		Host:    "127.0.0.1",
		Version: "1.0.0",
		StartedAt: fakeStarted.UTC().
			Format(time.RFC3339Nano),
	}
	data, _ := json.Marshal(sf)
	path := filepath.Join(dir, "server.59995.json")
	os.WriteFile(path, data, 0o644)

	assert.False(t, IsServerActive(dir), "expected false for same-boot PID reuse")

	// Stale file should be cleaned up.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr),
		"state file not cleaned up after PID reuse detection")
}

// TestIsStaleByProcessStart_OwnPID verifies that
// isStaleByProcessStart returns false for a state file that
// matches our own process start time.
func TestIsStaleByProcessStart_OwnPID(t *testing.T) {
	procStart, err := processStartTime(os.Getpid())
	if err != nil {
		t.Skipf("processStartTime not available: %v", err)
	}

	// Within tolerance — not stale.
	assert.False(t, isStaleByProcessStart(os.Getpid(), procStart),
		"expected false for matching start time")

	// Far off — stale.
	fakeTime := procStart.Add(-1 * time.Hour)
	assert.True(t, isStaleByProcessStart(os.Getpid(), fakeTime),
		"expected true for mismatched start time")
}

// TestStateFile_ReadOnlyPersisted verifies that
// WriteStateFile(readOnly=true) persists ReadOnly=true in the
// JSON payload so CLI consumers can distinguish pg serve
// (read-only) from local serve (read/write).
func TestStateFile_ReadOnlyPersisted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path, err := WriteStateFile(
		dir, "127.0.0.1", 9876, "test", true,
	)
	require.NoError(t, err)
	defer RemoveStateFile(dir, 9876)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf StateFile
	require.NoError(t, json.Unmarshal(raw, &sf))
	assert.True(t, sf.ReadOnly)
	assert.Equal(t, 9876, sf.Port)
	assert.Equal(t, "test", sf.Version)
}

// TestStateFile_ReadOnlyDefaultsToFalse verifies that
// WriteStateFile(readOnly=false) persists ReadOnly=false. The
// omitempty tag means the "read_only" key is elided from the
// JSON entirely in this case.
func TestStateFile_ReadOnlyDefaultsToFalse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path, err := WriteStateFile(
		dir, "127.0.0.1", 9877, "test", false,
	)
	require.NoError(t, err)
	defer RemoveStateFile(dir, 9877)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf StateFile
	require.NoError(t, json.Unmarshal(raw, &sf))
	assert.False(t, sf.ReadOnly)
}

func TestStateFileName(t *testing.T) {
	tests := []struct {
		port int
		want string
	}{
		{8080, "server.8080.json"},
		{3000, "server.3000.json"},
		{443, "server.443.json"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, stateFileName(tt.port),
			"stateFileName(%d)", tt.port)
	}
}
