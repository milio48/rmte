package main

// testKDFIterations keeps tests fast; production uses kdfIterations, which
// TestDeriveSessionKeysKnownAnswer exercises.
const testKDFIterations = 1000

// setupCrypto installs session keys derived from password (test iterations).
func setupCrypto(password string) error {
	k, err := deriveKeys(password, "test", testKDFIterations)
	if err != nil {
		return err
	}
	return installSessionKeys(k)
}

// generateAuthToken returns a relay auth token for password (test iterations).
func generateAuthToken(password string) string {
	k, err := deriveKeys(password, "test", testKDFIterations)
	if err != nil {
		panic(err)
	}
	return k.authToken
}
