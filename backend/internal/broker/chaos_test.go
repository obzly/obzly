package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// Broker fault drills (opt-in, minutes long): three brokers as separate
// processes of this test binary, publishers on the real PublishIngest path,
// and faults a host does to brokers - SIGKILL (power loss of that broker) and
// SIGSTOP/SIGCONT (a broker starved of CPU long enough to miss heartbeats).
// Afterwards every batch JetStream acked must still be in the stream.
//
//	OBZLY_CHAOS=freeze|kill|node|mixed OBZLY_CHAOS_DURATION=3m \
//	  go test ./internal/broker -run Test_Chaos_ -v -timeout 30m
//
// node is the homelab's node loss: the stream leader is killed, and while it
// is down the two survivors are frozen in turn, as a saturated host did.

type chaosBroker struct {
	cfg  ServerConfig
	cmd  *exec.Cmd
	dead bool
}

type chaosEvent struct {
	at   time.Time
	what string
}

type ackedBatch struct {
	id string
	at time.Time
}

// Test_ChaosBrokerChild is one broker process of a drill; it runs only when
// the drill starts it.
func Test_ChaosBrokerChild(t *testing.T) {
	encoded := os.Getenv("OBZLY_CHAOS_CHILD")
	if encoded == "" {
		t.Skip("a broker process of Test_Chaos_AckedBatchesSurviveBrokerFaults")
	}
	var cfg ServerConfig
	require.NoError(t, json.Unmarshal([]byte(encoded), &cfg))
	srv, err := StartServer(cfg, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	require.NoError(t, err)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	srv.Shutdown()
	srv.WaitForShutdown()
}

func Test_Chaos_AckedBatchesSurviveBrokerFaults(t *testing.T) {
	mode := os.Getenv("OBZLY_CHAOS")
	if mode == "" {
		t.Skip("set OBZLY_CHAOS=freeze|kill|node|mixed to run the broker fault drill")
	}
	duration := 3 * time.Minute
	if value := os.Getenv("OBZLY_CHAOS_DURATION"); value != "" {
		parsed, err := time.ParseDuration(value)
		require.NoError(t, err)
		duration = parsed
	}
	logDir := t.TempDir()
	if value := os.Getenv("OBZLY_CHAOS_LOGS"); value != "" {
		logDir = value
	}

	brokers, urls := startChaosBrokers(t, logDir)
	client, err := Connect(strings.Join(urls, ","), testToken, "chaos")
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx := context.Background()
	require.Eventually(t, func() bool {
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return client.EnsureIngestStreams(attempt, StreamSettings{Replicas: 3, MaxBytes: 8 << 30}) == nil
	}, time.Minute, 250*time.Millisecond)
	stream, err := client.JetStream.Stream(ctx, "INGEST_LOGS")
	require.NoError(t, err)
	waitSettled(t, stream)

	var (
		mu       sync.Mutex
		acked    []ackedBatch
		failures atomic.Int64
		events   []chaosEvent
	)
	record := func(format string, args ...any) {
		mu.Lock()
		events = append(events, chaosEvent{time.Now(), fmt.Sprintf(format, args...)})
		mu.Unlock()
	}
	projects := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	padding := strings.Repeat("x", 4<<10)
	publishCtx, stopPublishing := context.WithCancel(ctx)
	var publishers sync.WaitGroup
	for p := range 16 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			for n := 0; publishCtx.Err() == nil; n++ {
				id := fmt.Sprintf("%02d-%08d", p, n)
				// The receivers' publishTimeout: past it they answer 503.
				attempt, cancel := context.WithTimeout(publishCtx, 4*time.Second)
				err := client.PublishIngest(
					attempt,
					SignalLogs,
					projects[p%len(projects)],
					[][]byte{[]byte(id + padding)},
				)
				cancel()
				if err == nil {
					mu.Lock()
					acked = append(acked, ackedBatch{id, time.Now()})
					mu.Unlock()
				} else {
					failures.Add(1)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	runChaos(t, mode, duration, brokers, stream, logDir, record)
	stopPublishing()
	publishers.Wait()
	for i := range brokers {
		if brokers[i].dead {
			restartChaosBroker(t, &brokers[i], logDir)
			record("restart %s (end)", brokers[i].cfg.Name)
		}
	}
	waitSettled(t, stream)

	stored := readStoredIDs(t, stream)
	missing := []ackedBatch{}
	for _, batch := range acked {
		if stored[batch.id] == 0 {
			missing = append(missing, batch)
		}
	}
	duplicates := 0
	for _, copies := range stored {
		duplicates += copies - 1
	}
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	t.Logf(
		"mode %s, %s: %d acked, %d publishes failed, %d stored (%d ids), %d duplicates, %d acked but missing; stream lost %v",
		mode,
		duration,
		len(acked),
		failures.Load(),
		info.State.Msgs,
		len(stored),
		duplicates,
		len(missing),
		info.State.Lost,
	)
	for _, event := range events {
		t.Logf("  %s %s", event.at.Format("15:04:05.000"), event.what)
	}
	for i, batch := range missing {
		if i == 20 {
			t.Logf("  ... %d more", len(missing)-20)
			break
		}
		t.Logf("  missing %s acked at %s", batch.id, batch.at.Format("15:04:05.000"))
	}
	t.Logf("broker logs in %s", logDir)
	require.Empty(t, missing, "every batch JetStream acked is in the stream")
}

func startChaosBrokers(t *testing.T, logDir string) ([]chaosBroker, []string) {
	t.Helper()
	ports := freePorts(t, 6)
	var routes, urls []string
	for _, port := range ports[3:] {
		routes = append(routes, fmt.Sprintf("nats-route://127.0.0.1:%d", port))
	}
	brokers := make([]chaosBroker, 3)
	for i := range brokers {
		brokers[i].cfg = ServerConfig{
			Name: fmt.Sprintf("broker-%d", i), StoreDir: t.TempDir(), Token: testToken,
			ClientPort: ports[i], ClusterPort: ports[3+i], MonitorPort: -1,
			ClusterName: "obzly-chaos", Routes: routes,
		}
		urls = append(urls, fmt.Sprintf("nats://127.0.0.1:%d", ports[i]))
		restartChaosBroker(t, &brokers[i], logDir)
	}
	t.Cleanup(func() {
		for i := range brokers {
			if !brokers[i].dead {
				_ = brokers[i].cmd.Process.Signal(syscall.SIGCONT)
				_ = brokers[i].cmd.Process.Kill()
				_ = brokers[i].cmd.Wait()
			}
		}
	})
	return brokers, urls
}

func restartChaosBroker(t *testing.T, broker *chaosBroker, logDir string) {
	t.Helper()
	encoded, err := json.Marshal(broker.cfg)
	require.NoError(t, err)
	logFile, err := os.OpenFile(
		filepath.Join(logDir, broker.cfg.Name+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=^Test_ChaosBrokerChild$", "-test.timeout=0", "-test.v")
	cmd.Env = append(os.Environ(), "OBZLY_CHAOS_CHILD="+string(encoded), "OBZLY_CHAOS=")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	require.NoError(t, cmd.Start())
	broker.cmd, broker.dead = cmd, false
}

// runChaos applies mode's faults until duration has passed. At most one
// broker is dead at a time, as with R3 on three nodes losing one.
func runChaos(
	t *testing.T, mode string, duration time.Duration, brokers []chaosBroker,
	stream jetstream.Stream, logDir string, record func(string, ...any),
) {
	t.Helper()
	byName := map[string]int{}
	for i, broker := range brokers {
		byName[broker.cfg.Name] = i
	}
	leader := func() int {
		attempt, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		info, err := stream.Info(attempt)
		if err == nil && info.Cluster != nil {
			if i, ok := byName[info.Cluster.Leader]; ok && !brokers[i].dead {
				return i
			}
		}
		return liveBroker(brokers, -1)
	}
	freeze := func(i int, d time.Duration) {
		record("freeze %s for %s", brokers[i].cfg.Name, d.Round(time.Millisecond))
		_ = brokers[i].cmd.Process.Signal(syscall.SIGSTOP)
		time.Sleep(d)
		_ = brokers[i].cmd.Process.Signal(syscall.SIGCONT)
	}
	kill := func(i int) {
		record("kill %s", brokers[i].cfg.Name)
		_ = brokers[i].cmd.Process.Signal(syscall.SIGCONT)
		_ = brokers[i].cmd.Process.Kill()
		_ = brokers[i].cmd.Wait()
		brokers[i].dead = true
	}
	restart := func(i int) {
		restartChaosBroker(t, &brokers[i], logDir)
		record("restart %s", brokers[i].cfg.Name)
	}
	between := func(low, high time.Duration) time.Duration {
		return low + rand.N(high-low)
	}

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		step := mode
		if mode == "mixed" {
			step = []string{"freeze", "kill", "node"}[rand.N(3)]
		}
		switch step {
		case "freeze":
			target := leader()
			if rand.N(10) >= 7 {
				target = rand.N(len(brokers))
			}
			freeze(target, between(2*time.Second, 8*time.Second))
			time.Sleep(between(3*time.Second, 10*time.Second))
		case "kill":
			target := leader()
			kill(target)
			time.Sleep(between(5*time.Second, 20*time.Second))
			restart(target)
			time.Sleep(between(15*time.Second, 30*time.Second))
		case "node":
			down := leader()
			kill(down)
			until := time.Now().Add(60 * time.Second)
			for time.Now().Before(until) {
				freeze(liveBroker(brokers, down), between(time.Second, 6*time.Second))
				time.Sleep(between(time.Second, 5*time.Second))
			}
			restart(down)
			time.Sleep(30 * time.Second)
		default:
			t.Fatalf("unknown OBZLY_CHAOS mode %q", mode)
		}
	}
}

// liveBroker is a random broker that is alive, other than except.
func liveBroker(brokers []chaosBroker, except int) int {
	for {
		i := rand.N(len(brokers))
		if i != except && !brokers[i].dead {
			return i
		}
	}
}

// waitSettled waits for a stream leader with both followers current.
func waitSettled(t *testing.T, stream jetstream.Stream) {
	t.Helper()
	require.Eventually(t, func() bool {
		attempt, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		info, err := stream.Info(attempt)
		if err != nil || info.Cluster == nil || info.Cluster.Leader == "" || len(info.Cluster.Replicas) != 2 {
			return false
		}
		for _, replica := range info.Cluster.Replicas {
			if !replica.Current || replica.Offline {
				return false
			}
		}
		return true
	}, 3*time.Minute, 500*time.Millisecond, "stream leader and both followers current")
}

// readStoredIDs counts each batch id in the stream (a WorkQueue stream with
// no consumer keeps everything).
func readStoredIDs(t *testing.T, stream jetstream.Stream) map[string]int {
	t.Helper()
	ctx := context.Background()
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	stored := map[string]int{}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		var msg *jetstream.RawStreamMsg
		require.Eventually(t, func() bool {
			attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			msg, err = stream.GetMsg(attempt, seq)
			return err == nil || errors.Is(err, jetstream.ErrMsgNotFound)
		}, 30*time.Second, 100*time.Millisecond, "read seq %d", seq)
		if msg == nil || errors.Is(err, jetstream.ErrMsgNotFound) {
			continue
		}
		if len(msg.Data) >= 11 {
			stored[string(msg.Data[:11])]++
		}
	}
	return stored
}
