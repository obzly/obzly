package logs_core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_AutoContextScope_TheAnchorsWorkload(t *testing.T) {
	for name, tc := range map[string]struct {
		fields map[string]any
		extra  []string
		want   []string
	}{
		"deployment pod: its deployment, not the pod, replicaset or host": {
			fields: map[string]any{
				"k8s_cluster_name": "prod", "k8s_namespace_name": "shop", "k8s_deployment_name": "checkout",
				"k8s_replicaset_name": "checkout-7f9c", "k8s_pod_name": "checkout-7f9c-x1", "host_name": "node-1",
				"k8s_container_name": "app", "service_name": "checkout",
			},
			want: []string{"k8s_cluster_name", "k8s_namespace_name", "k8s_deployment_name", "k8s_container_name",
				"service_name"},
		},
		"statefulset": {
			fields: map[string]any{"k8s_namespace_name": "db", "k8s_statefulset_name": "pg", "k8s_pod_name": "pg-0"},
			want:   []string{"k8s_namespace_name", "k8s_statefulset_name"},
		},
		"cronjob pod: its job and cronjob": {
			fields: map[string]any{"k8s_namespace_name": "ops", "k8s_job_name": "backup-29", "k8s_cronjob_name": "backup",
				"k8s_pod_name": "backup-29-x"},
			want: []string{"k8s_namespace_name", "k8s_job_name", "k8s_cronjob_name"},
		},
		"bare pod: the pod, never the whole namespace": {
			fields: map[string]any{"k8s_namespace_name": "dev", "k8s_pod_name": "debug", "host_name": "node-2",
				"k8s_container_name": "shell"},
			want: []string{"k8s_namespace_name", "k8s_container_name", "k8s_pod_name"},
		},
		"outside Kubernetes: the host": {
			fields: map[string]any{"host_name": "vm-1", "service_name": "billing"},
			want:   []string{"service_name", "host_name"},
		},
		"configured extras, once": {
			fields: map[string]any{"k8s_namespace_name": "shop", "k8s_deployment_name": "cart", "team": "payments"},
			extra:  []string{"team", "k8s_namespace_name", "absent"},
			want:   []string{"k8s_namespace_name", "k8s_deployment_name", "team"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, AutoContextScope(&LogItemDTO{Fields: tc.fields}, tc.extra))
		})
	}
}

func Test_ContextExtraFields_ParsesTheCommaSeparatedList(t *testing.T) {
	assert.Equal(t, []string{"service_name", "team"}, ContextExtraFields(" service_name, team ,,service_name"))
	assert.Nil(t, ContextExtraFields(""))
}
