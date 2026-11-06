package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"obzly/internal/cloud/discovery"
	"obzly/internal/synthetics/spec"
)

func Test_CloudIntervals_DefaultTo4HoursAnd15Minutes(t *testing.T) {
	inventory, health := cloudIntervals(spec.CloudConnection{})
	require.Equal(t, 4*time.Hour, inventory)
	require.Equal(t, 15*time.Minute, health)

	inventory, health = cloudIntervals(spec.CloudConnection{IntervalSeconds: 1800, HealthIntervalSeconds: 3600})
	require.Equal(t, 30*time.Minute, inventory)
	require.Equal(t, 30*time.Minute, health, "never rarer than discovery")
}

func Test_ScanTimeout_FollowsTheInterval_WithinBounds(t *testing.T) {
	require.Equal(t, 2*time.Hour, scanTimeout(4*time.Hour, 5*time.Minute, 2*time.Hour))
	require.Equal(t, 48*time.Minute, scanTimeout(time.Hour, 5*time.Minute, 2*time.Hour))
	require.Equal(t, 12*time.Minute, scanTimeout(15*time.Minute, 2*time.Minute, 30*time.Minute))
	require.Equal(t, 5*time.Minute, scanTimeout(time.Minute, 5*time.Minute, 2*time.Hour))
}

func Test_CloudState_SurvivesARestart_UntilTheScopeChanges(t *testing.T) {
	p := testProbe(t)
	connection := spec.CloudConnection{
		ID: "conn-1", Name: "Contoso prod", Provider: spec.ProviderAWS, Scope: "account", ScopeID: "123456789012",
		Regions: []string{"us-east-1"}, IntervalSeconds: 14400,
	}
	at := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	p.saveCloudState(connection, &cloudState{
		Scope: cloudScope(connection), InventoryAt: at,
		Resources: []discovery.Resource{{ID: "i-0abc", Type: "aws_ec2_instance", Health: discovery.HealthHealthy}},
	}, [32]byte{})

	renamed := connection
	renamed.Name, renamed.HealthIntervalSeconds = "Contoso production", 600
	state := p.loadCloudState(renamed)
	require.NotNil(t, state, "a new name or interval keeps the inventory")
	require.Equal(t, at, state.InventoryAt)
	require.Equal(t, discovery.HealthHealthy, state.Resources[0].Health)

	moved := connection
	moved.Regions = []string{"eu-west-1"}
	require.Nil(t, p.loadCloudState(moved), "other regions need a new discovery")
}

func Test_SaveCloudState_Unchanged_IsNotRewritten(t *testing.T) {
	p := testProbe(t)
	connection := spec.CloudConnection{ID: "conn-2", Provider: spec.ProviderAWS}
	state := &cloudState{Scope: cloudScope(connection), Resources: []discovery.Resource{{ID: "i-0abc"}}}

	digest := p.saveCloudState(connection, state, [32]byte{})
	require.NoError(t, os.Remove(p.cloudStatePath(connection)))
	require.Equal(t, digest, p.saveCloudState(connection, state, digest))
	require.NoFileExists(t, p.cloudStatePath(connection), "the same state isn't written again")

	state.Resources[0].Health = discovery.HealthUnhealthy
	require.NotEqual(t, digest, p.saveCloudState(connection, state, digest))
	require.FileExists(t, p.cloudStatePath(connection))
}

func Test_TaskCloudToken_AsksForTheTasksOwnToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/synthetics/probe/tasks/task-1/cloud-token", r.URL.Path)
		require.Equal(t, "Bearer obz_prb_test", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(spec.CloudToken{Token: "signed", ExpiresAt: time.Now().Add(10 * time.Minute)})
	}))
	defer server.Close()
	api := &controlPlane{base: server.URL, token: "obz_prb_test", http: server.Client()}

	token, err := api.taskCloudToken(context.Background(), "task-1")

	require.NoError(t, err)
	require.Equal(t, "signed", token.Token)
}

func Test_CloudTokenCache_ReusesATokenUntilShortlyBeforeItExpires(t *testing.T) {
	var cache cloudTokenCache
	var fetched atomic.Int32
	expiresIn := 10 * time.Minute
	fetch := func(context.Context) (spec.CloudToken, error) {
		fetched.Add(1)
		return spec.CloudToken{Token: "t", ExpiresAt: time.Now().UTC().Add(expiresIn)}, nil
	}

	for range 3 {
		token, err := cache.get(context.Background(), "conn-1", fetch)
		require.NoError(t, err)
		require.Equal(t, "t", token)
	}
	require.EqualValues(t, 1, fetched.Load())

	expiresIn = time.Minute
	_, _ = cache.get(context.Background(), "conn-2", fetch)
	_, _ = cache.get(context.Background(), "conn-2", fetch)
	require.EqualValues(t, 3, fetched.Load(), "a token about to expire is replaced")
}
