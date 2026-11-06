package cache

import (
	"io"
	"log/slog"
	"net"
	"obzly/internal/config"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

func Test_ConnectWithRetry_WhenValkeyIsUp_Connects(t *testing.T) {
	env := config.GetEnv()

	client, err := connectWithRetry(valkey.ClientOption{
		InitAddress: []string{env.ValkeyHost + ":" + env.ValkeyPort},
		Password:    env.ValkeyPassword,
		Username:    env.ValkeyUsername,
	}, 5*time.Second, quietLogger())

	require.NoError(t, err)
	client.Close()
}

func Test_ConnectWithRetry_WhenValkeyNeverComesUp_GivesUpWithinTheBudget(t *testing.T) {
	started := time.Now().UTC()

	_, err := connectWithRetry(valkey.ClientOption{
		InitAddress: []string{closedAddress(t)},
	}, 3*time.Second, quietLogger())

	require.Error(t, err)
	require.Less(t, time.Since(started), 5*time.Second)
}

func closedAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
