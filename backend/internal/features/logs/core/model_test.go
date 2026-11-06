package logs_core

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// A Live Tail row must carry client_ip in Fields like a stored search's
// row does, or the results table's client_ip column is empty.
func Test_LogItemToDTO_CopiesClientIPIntoFields(t *testing.T) {
	withFields := LogItem{
		ID:       uuid.New(),
		Message:  "m",
		ClientIP: "203.0.113.7",
		Fields:   map[string]any{"service": "api"},
	}
	dto := withFields.ToDTO()
	assert.Equal(t, "203.0.113.7", dto.ClientIP)
	assert.Equal(t, "203.0.113.7", dto.Fields["client_ip"])
	assert.Equal(t, "api", dto.Fields["service"])

	noFields := LogItem{ID: uuid.New(), Message: "m", ClientIP: "203.0.113.8"}
	assert.Equal(t, "203.0.113.8", noFields.ToDTO().Fields["client_ip"])

	noIP := LogItem{ID: uuid.New(), Message: "m"}
	assert.Nil(t, noIP.ToDTO().Fields, "nothing to add - no empty map")

	// A custom field of the same name wins, as in the stored-row path.
	custom := LogItem{
		ID:       uuid.New(),
		Message:  "m",
		ClientIP: "203.0.113.9",
		Fields:   map[string]any{"client_ip": "10.1.1.1"},
	}
	assert.Equal(t, "10.1.1.1", custom.ToDTO().Fields["client_ip"])
}
