package model

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserListingDoesNotExposeManagementTokens(t *testing.T) {
	db := setupQuotaTest(t)
	t.Setenv("USER_LISTING_CANARY", "listing-canary-1")
	canary := os.Getenv("USER_LISTING_CANARY")
	require.NoError(t, db.Model(&User{}).Where("id = ?", 1).Updates(map[string]any{
		"access_token": canary,
		"password":     canary,
	}).Error)

	users, err := GetAllUsers(0, 10, "")
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Empty(t, users[0].AccessToken)
	require.Empty(t, users[0].Password)

	users, err = SearchUsers("quota-user")
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Empty(t, users[0].AccessToken)
	require.Empty(t, users[0].Password)
}
