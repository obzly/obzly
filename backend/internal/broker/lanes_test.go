package broker

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A project's lanes are distinct shards, lane 0 its own.
func Test_LaneShard_LaneZeroIsTheProjectsShardAndLanesNeverCollide(t *testing.T) {
	for range 200 {
		project := uuid.New()
		require.Equal(t, ShardFor(project), LaneShard(project, 0))
		seen := map[int]bool{}
		for lane := range IngestShards {
			shard := LaneShard(project, lane)
			require.False(t, seen[shard], "lane %d", lane)
			seen[shard] = true
		}
	}
}

// With three lanes, a project's batches go round-robin to its three lane
// shards; a project without lanes stays on its own shard.
func Test_PublishIngest_ProjectWithLanes_SpreadsBatchesOverItsLaneShards(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	busy, quiet := uuid.New(), uuid.New()
	withLanes(t, SignalLogs, busy, 3)

	for i := range 30 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, busy, [][]byte{fmt.Appendf(nil, "b-%d", i)}))
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, quiet, [][]byte{fmt.Appendf(nil, "q-%d", i)}))
	}

	for lane := range 3 {
		require.Equal(t, 10, subjectCount(t, client, fmt.Sprintf("ingest.logs.%d.%s", LaneShard(busy, lane), busy)),
			"lane %d", lane)
	}
	require.Equal(t, 30, subjectCount(t, client, IngestSubject(SignalLogs, quiet)))
}

// A lane whose shard is behind gets no new batches while another keeps up;
// with every lane equally behind, round-robin over all of them again.
func Test_PublishIngest_LaneBehind_BatchesGoToTheOtherLanes(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	project := uuid.New()
	behind := LaneShard(project, 0)
	withLanesBacklog(t, SignalLogs, project, 3, map[int]float64{behind: 90})

	for i := range 30 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{fmt.Appendf(nil, "b-%d", i)}))
	}

	require.Zero(t, subjectCount(t, client, fmt.Sprintf("ingest.logs.%d.%s", behind, project)))
	for lane := 1; lane < 3; lane++ {
		require.Equal(t, 15, subjectCount(t, client, fmt.Sprintf("ingest.logs.%d.%s", LaneShard(project, lane), project)),
			"lane %d", lane)
	}

	every := map[int]float64{}
	seen := map[int]bool{}
	for lane := range 3 {
		every[LaneShard(project, lane)] = 90
	}
	withLanesBacklog(t, SignalLogs, project, 3, every)
	for range 3 {
		seen[laneSource.Load().shardFor(SignalLogs, project)] = true
	}
	require.Len(t, seen, 3)
}

// A lane filling up gets fewer new batches well before it is behind: 10 s
// of backlog halves its share, 30 s quarters it. Round-robin gave it 1/K
// until it crossed 60 s.
func Test_PublishIngest_LaneFillingUp_GetsFewerBatches(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	project := uuid.New()
	filling, other := LaneShard(project, 0), LaneShard(project, 1)
	withLanesBacklog(t, SignalLogs, project, 2, map[int]float64{filling: 10})

	for i := range 30 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{fmt.Appendf(nil, "b-%d", i)}))
	}

	require.Equal(t, 10, subjectCount(t, client, fmt.Sprintf("ingest.logs.%d.%s", filling, project)))
	require.Equal(t, 20, subjectCount(t, client, fmt.Sprintf("ingest.logs.%d.%s", other, project)))
	require.Equal(t, []int{5, 20}, laneWeights(project, 2, map[int]float64{filling: 30}))
}

// Weights only shift for a real backlog: a healthy shard's second or so of
// pending batches leaves plain round-robin, and with every lane behind the
// least behind gets the most.
func Test_LaneWeights_NoiseIgnored_AndLeastBehindPreferredWhenAllAre(t *testing.T) {
	project := uuid.New()
	first, second := LaneShard(project, 0), LaneShard(project, 1)

	require.Equal(t, []int{first, second}, laneWheel(project, 2, map[int]float64{first: 1.5}))

	weights := laneWeights(project, 2, map[int]float64{first: 120, second: 60})
	require.Greater(t, weights[1], weights[0])
	require.Positive(t, weights[0], "every lane behind: none is skipped")
}

// Two projects with lanes publishing in turn each spread over both their
// lanes: with one counter shared by every project, each landed on one lane.
func Test_ShardFor_TwoProjectsInTurn_EachSpreadsOverItsLanes(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	cache := &LaneCache{}
	cache.set(map[string]map[uuid.UUID]int{SignalLogs: {a: 2, b: 2}}, nil)

	seenA, seenB := map[int]int{}, map[int]int{}
	for range 10 {
		seenA[cache.shardFor(SignalLogs, a)]++
		seenB[cache.shardFor(SignalLogs, b)]++
	}

	require.Equal(t, map[int]int{LaneShard(a, 0): 5, LaneShard(a, 1): 5}, seenA)
	require.Equal(t, map[int]int{LaneShard(b, 0): 5, LaneShard(b, 1): 5}, seenB)
}

// The backlog survives a write and read through Valkey's string.
func Test_ParseBacklog_ReadsWhatFormatBacklogWrites(t *testing.T) {
	backlog := map[int]float64{32: 4, 15: 72, 0: 61}
	require.Equal(t, "0:61,15:72,32:4", formatBacklog(backlog))
	require.Equal(t, backlog, parseBacklog(formatBacklog(backlog)))
	require.Equal(t, map[int]float64{3: 9}, parseBacklog("3:9, x:1,64:2,-1:3,5:-2,7"))
}

// The tail reads a project's pending batches from every lane, oldest first,
// and nothing of another project.
func Test_PendingIngest_ProjectWithLanes_ReadsEveryLaneInOrder(t *testing.T) {
	client := startTestClient(t, 64<<20)
	ctx := context.Background()
	project, other := uuid.New(), uuid.New()
	withLanes(t, SignalLogs, project, 4)
	for i := range 40 {
		require.NoError(t, client.PublishIngest(ctx, SignalLogs, project, [][]byte{fmt.Appendf(nil, "p-%d", i)}))
	}
	require.NoError(t, client.PublishIngest(ctx, SignalLogs, other, [][]byte{[]byte("other")}))

	pending, err := client.PendingIngest(ctx, SignalLogs, project, 64<<20)

	require.NoError(t, err)
	require.Len(t, pending, 40)
	for i, message := range pending {
		require.Equal(t, fmt.Sprintf("p-%d", i), string(message.Data))
	}
	oldest := pending[0].Subject
	require.Contains(t, laneSubjects(project, 4), oldest)
	firsts, err := client.FirstPendingSeqs(ctx, SignalLogs, []string{oldest, "ingest.logs.0.none"})
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{oldest: pending[0].Seq}, firsts)
}

// withLanes gives project lanes on signal for the test.
func withLanes(t *testing.T, signal string, project uuid.UUID, lanes int) {
	t.Helper()
	withLanesBacklog(t, signal, project, lanes, nil)
}

// withLanesBacklog is withLanes with signal's shards behind by backlog
// seconds.
func withLanesBacklog(t *testing.T, signal string, project uuid.UUID, lanes int, backlog map[int]float64) {
	t.Helper()
	cache := &LaneCache{}
	cache.set(map[string]map[uuid.UUID]int{signal: {project: lanes}}, map[string]map[int]float64{signal: backlog})
	SetLaneCache(cache)
	t.Cleanup(func() { SetLaneCache(nil) })
}

func subjectCount(t *testing.T, client *Client, subject string) int {
	t.Helper()
	messages, _, err := client.directGetBatch(context.Background(), "INGEST_LOGS", subject, 1, 0, 1000)
	require.NoError(t, err)
	return len(messages)
}

func laneSubjects(project uuid.UUID, lanes int) []string {
	var subjects []string
	for lane := range lanes {
		subjects = append(subjects, fmt.Sprintf("ingest.logs.%d.%s", LaneShard(project, lane), project))
	}
	return subjects
}
