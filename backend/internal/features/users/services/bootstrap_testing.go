package users_services

// CreateInitialAdminForTest is CreateInitialAdmin that also returns the raw
// bootstrap token, for tests of SetRootAdminPassword.
func (s *UserService) CreateInitialAdminForTest() (rawBootstrapToken string, err error) {
	email := "admin"

	rawToken, tokenHash, err := generateBootstrapToken()
	if err != nil {
		return "", err
	}

	created, err := s.userRepository.CreateInitialAdmin(email, tokenHash)
	if err != nil {
		return "", err
	}
	if !created {
		return "", nil
	}

	return rawToken, nil
}

// SetTokenRevocationCheckForTest replaces the Valkey revocation lookup, so a
// test can make it fail like an unreachable Valkey; the returned func
// restores it.
func SetTokenRevocationCheckForTest(check func(jti string) (bool, error)) (restore func()) {
	previous := checkTokenRevoked
	checkTokenRevoked = check
	return func() { checkTokenRevoked = previous }
}
