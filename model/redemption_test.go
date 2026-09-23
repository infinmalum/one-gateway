package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRedemptionCanOnlyBeUsedOnce(t *testing.T) {
	previousDB, previousLogDB := DB, LOG_DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Redemption{}, &Log{}))
	DB, LOG_DB = db, db
	t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })

	require.NoError(t, db.Create(&User{Id: 1, Username: "redeem-user", Password: "password123"}).Error)
	require.NoError(t, db.Create(&Redemption{Key: "one-use-code", Status: RedemptionCodeStatusEnabled, Quota: 20}).Error)

	quota, err := Redeem(context.Background(), "one-use-code", 1)
	require.NoError(t, err)
	require.Equal(t, int64(20), quota)
	_, err = Redeem(context.Background(), "one-use-code", 1)
	require.Error(t, err)

	var user User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, int64(20), user.Quota)
}
