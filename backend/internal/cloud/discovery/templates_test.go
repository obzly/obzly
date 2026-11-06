package discovery

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The connection wizard offers the same templates as deploy/cloud; they
// ship with the frontend build, which can't read deploy/.
func Test_CloudTemplates_TheFrontendCopies_MatchDeploy(t *testing.T) {
	for source, copied := range map[string]string{
		"aws/obzly-readonly-role.yaml": "obzly-readonly-role.yaml",
		"azure/obzly-reader.bicep":     "obzly-reader.bicep",
		"azure/obzly-identity.bicep":   "obzly-identity.bicep",
	} {
		want, err := os.ReadFile("../../../../deploy/cloud/" + source)
		require.NoError(t, err)
		got, err := os.ReadFile("../../../../frontend/src/features/infrastructure/templates/" + copied)
		require.NoError(t, err)
		require.Equal(t, string(want), string(got), "copy deploy/cloud/%s over the frontend's copy", source)
	}
}
