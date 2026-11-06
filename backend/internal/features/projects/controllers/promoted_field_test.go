package projects_controllers

import (
	"net/http"
	"testing"

	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_PromoteField_HandsOutSlotsByType_AndNeverReusesARetiredOne(t *testing.T) {
	router := projects_testing.CreateTestRouter(GetProjectController(), GetMembershipController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Promoted fields", owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	url := "/api/v1/projects/" + project.ID.String() + "/promoted-fields"
	promote := func(field, fieldType string, status int) projects_models.PromotedField {
		var promoted projects_models.PromotedField
		if status == http.StatusOK {
			test_utils.MakePostRequestAndUnmarshal(t, router, url, "Bearer "+owner.Token,
				PromoteFieldRequest{Field: field, Type: fieldType}, status, &promoted)
		} else {
			test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token,
				PromoteFieldRequest{Field: field, Type: fieldType}, status)
		}
		return promoted
	}

	assert.Equal(t, "s1", promote("http.status", "string", http.StatusOK).Slot)
	assert.Equal(t, "n1", promote("latency_ms", "number", http.StatusOK).Slot)
	promote("http_status", "string", http.StatusBadRequest)  // already promoted (names normalize)
	promote("service.name", "string", http.StatusBadRequest) // has its own column
	promote("other", "boolean", http.StatusBadRequest)

	test_utils.MakeDeleteRequest(t, router, url+"/http_status", "Bearer "+owner.Token, http.StatusOK)
	test_utils.MakeDeleteRequest(t, router, url+"/http_status", "Bearer "+owner.Token, http.StatusNotFound)
	assert.Equal(t, "s2", promote("http_status", "string", http.StatusOK).Slot, "s1 stays retired")

	var listed []projects_models.PromotedField
	test_utils.MakeGetRequestAndUnmarshal(t, router, url, "Bearer "+member.Token, http.StatusOK, &listed)
	require.Len(t, listed, 2)

	test_utils.MakePostRequest(t, router, url, "Bearer "+member.Token,
		PromoteFieldRequest{Field: "x", Type: "string"}, http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, url+"/latency_ms", "Bearer "+member.Token, http.StatusForbidden)

	for i := 2; i <= 4; i++ {
		promote("number_"+string(rune('a'+i)), "number", http.StatusOK)
	}
	promote("one_too_many", "number", http.StatusBadRequest)
}
