package servicemap

import (
	"testing"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/stretchr/testify/assert"
)

func flowSeries(host, src, srcType, dst, dstType string) metrics_core.SeriesDTO {
	return metrics_core.SeriesDTO{Labels: map[string]string{
		labelHostID:       host,
		labelSrcOwnerName: src,
		labelSrcOwnerType: srcType,
		labelDstOwnerName: dst,
		labelDstOwnerType: dstType,
	}}
}

func Test_dropServiceAliases_ServiceDuplicateOfWorkload_Dropped(t *testing.T) {
	series := []metrics_core.SeriesDTO{
		flowSeries("n1", "frontend", "Deployment", "coredns", "Deployment"),
		flowSeries("n1", "frontend", "Deployment", "kube-dns", "Service"),
		flowSeries("n1", "redis", "Deployment", "frontend", "Deployment"),
		flowSeries("n1", "svc-redis", "Service", "frontend", "Deployment"),
	}

	kept := dropServiceAliases(series)

	assert.Len(t, kept, 2)
	for _, s := range kept {
		assert.NotEqual(t, ownerTypeService, s.Labels[labelSrcOwnerType])
		assert.NotEqual(t, ownerTypeService, s.Labels[labelDstOwnerType])
	}
}

func Test_dropServiceAliases_ServiceIsOnlyView_Kept(t *testing.T) {
	series := []metrics_core.SeriesDTO{
		flowSeries("n1", "cert-manager", "Deployment", "", ""),
		flowSeries("n1", "cert-manager", "Deployment", "kubernetes", "Service"),
	}

	kept := dropServiceAliases(series)

	assert.Len(t, kept, 2)
}

func Test_dropServiceAliases_WorkloadOnDifferentNode_ServiceKept(t *testing.T) {
	series := []metrics_core.SeriesDTO{
		flowSeries("n1", "frontend", "Deployment", "coredns", "Deployment"),
		flowSeries("n2", "frontend", "Deployment", "kube-dns", "Service"),
	}

	kept := dropServiceAliases(series)

	assert.Len(t, kept, 2)
}
