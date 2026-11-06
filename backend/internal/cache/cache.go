package cache

import (
	"crypto/tls"
	"log/slog"
	"obzly/internal/config"
	"obzly/internal/util/logger"
	"os"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// startupWait bounds how long a starting process waits for Valkey (it may
// still be starting, or moving with a replaced node). It stays under the
// api/control-plane startup probe budget (5 s x 42), and it is bounded at all
// because workers have no probes: an endless wait would hang as Running.
const startupWait = 3 * time.Minute

const maxRetryDelay = 15 * time.Second

var (
	once         sync.Once
	valkeyClient valkey.Client
)

func GetCache() valkey.Client {
	once.Do(func() {
		env := config.GetEnv()

		options := valkey.ClientOption{
			InitAddress: []string{env.ValkeyHost + ":" + env.ValkeyPort},
			Password:    env.ValkeyPassword,
			Username:    env.ValkeyUsername,
		}

		if env.ValkeyIsSsl {
			options.TLSConfig = &tls.Config{
				ServerName: env.ValkeyHost,
			}
		}

		log := logger.GetLogger()
		client, err := connectWithRetry(options, startupWait, log)
		if err != nil {
			log.Error("Valkey is unreachable, giving up",
				"address", options.InitAddress, "waited", startupWait, "error", err)
			os.Exit(1)
		}

		valkeyClient = client
	})

	return valkeyClient
}

func connectWithRetry(options valkey.ClientOption, budget time.Duration, log *slog.Logger) (valkey.Client, error) {
	deadline := time.Now().UTC().Add(budget)
	delay := time.Second
	for attempt := 1; ; attempt++ {
		client, err := valkey.NewClient(options)
		if err == nil {
			if attempt > 1 {
				log.Info("Connected to Valkey", "address", options.InitAddress, "attempts", attempt)
			}
			return client, nil
		}
		if time.Now().UTC().Add(delay).After(deadline) {
			return nil, err
		}
		log.Warn(
			"Valkey isn't reachable yet, retrying",
			"address",
			options.InitAddress,
			"attempt",
			attempt,
			"retry_in",
			delay,
			"error",
			err,
		)
		time.Sleep(delay)
		delay = min(delay*2, maxRetryDelay)
	}
}
