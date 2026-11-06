package otlp_logs

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func Test_GrpcStatusFor_IngestBufferErrors_AreRetryableForOtlpExporters(t *testing.T) {
	full := status.Convert(grpcStatusFor(&ExportError{Code: ExportErrorResourceExhausted, Message: "full"}))
	require.Equal(t, codes.ResourceExhausted, full.Code())
	require.Len(t, full.Details(), 1, "exporters retry RESOURCE_EXHAUSTED only with RetryInfo")
	retryInfo, ok := full.Details()[0].(*errdetails.RetryInfo)
	require.True(t, ok)
	require.Positive(t, retryInfo.GetRetryDelay().AsDuration())

	unavailable := status.Convert(grpcStatusFor(&ExportError{Code: ExportErrorUnavailable, Message: "down"}))
	require.Equal(t, codes.Unavailable, unavailable.Code())
}
