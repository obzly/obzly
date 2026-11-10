package savedviews_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	projects_testing "obzly/internal/features/projects/testing"
	"obzly/internal/features/savedviews"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"
)

func Test_SavedViews_PrivateOrShared_OwnerOnlyChanges_AndADefaultPerKind(t *testing.T) {
	router := projects_testing.CreateTestRouter(savedviews.GetController())
	owner := "Bearer " + users_testing.CreateTestUser(users_enums.UserRoleMember).Token
	reader := "Bearer " + users_testing.CreateTestUser(users_enums.UserRoleMember).Token
	state := json.RawMessage(`{"range":"last6m","gran":"monthly","f":{"provider":["azure"]}}`)

	var shared, private savedviews.ViewDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/saved-views", owner,
		savedviews.ViewRequest{Kind: "cost", Name: "Azure spend by service", Shared: true, State: state},
		http.StatusOK, &shared)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/saved-views", owner,
		savedviews.ViewRequest{Kind: "cost", Name: "Mine only", State: state}, http.StatusOK, &private)
	test_utils.MakePostRequest(t, router, "/api/v1/saved-views", owner,
		savedviews.ViewRequest{Kind: "cost", Name: "mine ONLY", State: state}, http.StatusBadRequest)
	test_utils.MakePostRequest(t, router, "/api/v1/saved-views", owner,
		savedviews.ViewRequest{Kind: "logs", Name: "Wrong kind", State: state}, http.StatusBadRequest)
	test_utils.MakePostRequest(t, router, "/api/v1/saved-views", owner,
		savedviews.ViewRequest{Kind: "cost", Name: "Not an object", State: json.RawMessage(`[1,2]`)},
		http.StatusBadRequest)

	var readerList savedviews.ViewList
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/saved-views?kind=cost", reader, http.StatusOK,
		&readerList)
	require.Len(t, readerList.Views, 1, "a private view stays private")
	assert.Equal(t, "Azure spend by service", readerList.Views[0].Name)
	assert.False(t, readerList.Views[0].Mine)
	assert.JSONEq(t, string(state), string(readerList.Views[0].State))

	test_utils.MakePutRequest(t, router, "/api/v1/saved-views/"+shared.ID.String(), reader,
		savedviews.ViewRequest{Name: "Taken over", State: state}, http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/saved-views/"+private.ID.String(), reader, http.StatusNotFound)

	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/saved-views/default", reader,
		savedviews.DefaultRequest{Kind: "cost", ViewID: &shared.ID}, http.StatusOK, &readerList)
	require.NotNil(t, readerList.DefaultID)
	assert.Equal(t, shared.ID, *readerList.DefaultID)
	test_utils.MakePutRequest(t, router, "/api/v1/saved-views/default", reader,
		savedviews.DefaultRequest{Kind: "cost", ViewID: &private.ID}, http.StatusNotFound)

	var renamed savedviews.ViewDTO
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/saved-views/"+shared.ID.String(), owner,
		savedviews.ViewRequest{Name: "Azure by service", Shared: true, State: state}, http.StatusOK, &renamed)
	assert.Equal(t, "Azure by service", renamed.Name)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/saved-views/"+shared.ID.String(), owner, http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/saved-views?kind=cost", reader, http.StatusOK,
		&readerList)
	assert.Empty(t, readerList.Views)
	assert.Nil(t, readerList.DefaultID, "a deleted view is no one's default")
}
