package users_testing

import (
	"fmt"
	"strings"
	"time"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

// CreateTestUser creates a user and issues a token via GenerateAccessToken,
// bypassing SignIn. Admin-role users are marked MFA-enabled so they pass
// RequireMFAIfEnforced.
func CreateTestUser(role users_enums.UserRole) *users_dto.SignInResponseDTO {
	userID := uuid.New()
	// The whole id: the shared test database keeps every run's users, and a short
	// prefix collides.
	email := fmt.Sprintf("%s-%s@test.com", strings.ToLower(string(role)), userID)

	hashedPassword := "$2a$10$test"
	user := &users_models.User{
		ID:                   userID,
		Email:                email,
		Name:                 "Test User",
		HashedPassword:       &hashedPassword,
		PasswordCreationTime: time.Now().UTC(),
		CreatedAt:            time.Now().UTC(),
		Role:                 role,
		Status:               users_enums.UserStatusActive,
	}

	userRepository := &users_repositories.UserRepository{}
	err := userRepository.CreateUser(user)
	if err != nil {
		panic(err)
	}

	if role == users_enums.UserRoleAdmin {
		if err := userRepository.EnableMFA(userID, 0); err != nil {
			panic(err)
		}
	}

	response, err := users_services.GetUserService().GenerateAccessToken(user)
	if err != nil {
		panic(err)
	}

	response.Email = user.Email

	return response
}

// EnableRealMFAForTestUser gives the user a real TOTP secret and returns a valid
// code, for tests that need a real step-up check.
func EnableRealMFAForTestUser(userID uuid.UUID, email string) string {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Obzly", AccountName: email})
	if err != nil {
		panic(err)
	}

	secretKeyRepository := &users_repositories.SecretKeyRepository{}
	encrypted, err := secretKeyRepository.Encrypt(key.Secret())
	if err != nil {
		panic(err)
	}

	userRepository := &users_repositories.UserRepository{}
	if err := userRepository.SetPendingMFASecret(userID, encrypted); err != nil {
		panic(err)
	}
	if err := userRepository.EnableMFA(userID, 0); err != nil {
		panic(err)
	}

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		panic(err)
	}
	return code
}

func ReacreateInitAdminAndGetAccess() *users_dto.SignInResponseDTO {
	RecreateInitialAdmin()

	userRepository := &users_repositories.UserRepository{}
	user, err := userRepository.GetRootAdmin()
	if err != nil {
		panic(err)
	}

	if err := userRepository.EnableMFA(user.ID, 0); err != nil {
		panic(err)
	}
	user.MFAEnabled = true

	response, err := users_services.GetUserService().GenerateAccessToken(user)
	if err != nil {
		panic(err)
	}

	return response
}

// RecreateInitialAdmin replaces the root admin and returns its bootstrap token.
// The root admin is shared by every test package, so the caller's TestMain must
// hold test_utils.RootAdminLock for its whole run.
func RecreateInitialAdmin() string {
	userRepository := &users_repositories.UserRepository{}
	err := userRepository.RetireRootAdminForTests("admin-" + uuid.New().String())
	if err != nil {
		panic(err)
	}

	userService := users_services.GetUserService()
	token, err := userService.CreateInitialAdminForTest()
	if err != nil {
		panic(err)
	}
	return token
}
