package segments_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/util/querylimit"

	"github.com/stretchr/testify/require"
)

const mb = int64(1_000_000)

func Test_MemoryGate_RoomFree_AdmitsTheFullCap(t *testing.T) {
	gate := segments.NewMemoryGate(3000*mb, 256*mb, time.Second)
	gate.SetInUse(500 * mb)

	limit, release, err := gate.Admit(context.Background(), 1600*mb)

	require.NoError(t, err)
	require.Equal(t, 1600*mb, limit)
	require.Equal(t, 1600*mb, gate.Admitted())
	release()
	require.Zero(t, gate.Admitted())
}

// Long-lived handles in use and another sandbox leave less than a cap: the
// next sandbox gets what is free and spills past it.
func Test_MemoryGate_LessThanACapFree_AdmitsWhatIsFree(t *testing.T) {
	gate := segments.NewMemoryGate(3000*mb, 256*mb, time.Second)
	gate.SetInUse(400 * mb)
	_, first, err := gate.Admit(context.Background(), 1600*mb)
	require.NoError(t, err)
	defer first()

	limit, release, err := gate.Admit(context.Background(), 1600*mb)

	require.NoError(t, err)
	defer release()
	require.Equal(t, 1000*mb, limit)
}

func Test_MemoryGate_LessThanTheFloorFree_WaitsForARelease(t *testing.T) {
	gate := segments.NewMemoryGate(1800*mb, 256*mb, 5*time.Second)
	_, first, err := gate.Admit(context.Background(), 1600*mb)
	require.NoError(t, err)
	go func() {
		time.Sleep(100 * time.Millisecond)
		first()
	}()

	started := time.Now()
	limit, release, err := gate.Admit(context.Background(), 1600*mb)

	require.NoError(t, err)
	defer release()
	require.Equal(t, 1600*mb, limit)
	require.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond)
}

func Test_MemoryGate_NoRoomWithinTheWait_RefusesAsTooManyQueries(t *testing.T) {
	gate := segments.NewMemoryGate(1800*mb, 256*mb, 100*time.Millisecond)
	_, first, err := gate.Admit(context.Background(), 1600*mb)
	require.NoError(t, err)
	defer first()

	_, _, err = gate.Admit(context.Background(), 1600*mb)

	require.ErrorIs(t, err, segments.ErrMemoryBusy)
	require.True(t, errors.Is(err, querylimit.ErrTooManyQueries), "answered 429 like other query limits")
}

// Busy long-lived handles alone never keep a lone sandbox out.
func Test_MemoryGate_NoSandboxRunning_AdmitsAtLeastTheFloor(t *testing.T) {
	gate := segments.NewMemoryGate(1000*mb, 256*mb, 100*time.Millisecond)
	gate.SetInUse(1200 * mb)

	limit, release, err := gate.Admit(context.Background(), 1600*mb)

	require.NoError(t, err)
	defer release()
	require.Equal(t, 256*mb, limit)
}

func Test_MemoryGate_None_AdmitsTheFullCap(t *testing.T) {
	var gate *segments.MemoryGate

	limit, release, err := gate.Admit(context.Background(), 1600*mb)

	require.NoError(t, err)
	release()
	require.Equal(t, 1600*mb, limit)
}
