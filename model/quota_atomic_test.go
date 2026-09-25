package model

import (
	"testing"

	"github.com/infinmalum/one-gateway/common/config"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupQuotaTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousBatch := config.BatchUpdateEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))
	DB = db
	config.BatchUpdateEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		config.BatchUpdateEnabled = previousBatch
	})
	require.NoError(t, db.Create(&User{Id: 1, Username: "quota-user", Password: "password123", Quota: 5}).Error)
	require.NoError(t, db.Create(&Token{Id: 1, UserId: 1, Key: "quota-token", RemainQuota: 10}).Error)
	return db
}

func TestQuotaDeductionRejectsOverdraftWithBatchEnabled(t *testing.T) {
	db := setupQuotaTest(t)
	config.BatchUpdateEnabled = true

	require.NoError(t, DecreaseTokenQuota(1, 3))
	require.NoError(t, DecreaseUserQuota(1, 3))
	require.Error(t, DecreaseUserQuota(1, 3))
	require.Error(t, DecreaseTokenQuota(1, 8))

	var user User
	var token Token
	require.NoError(t, db.First(&user, 1).Error)
	require.NoError(t, db.First(&token, 1).Error)
	require.Equal(t, int64(2), user.Quota)
	require.Equal(t, int64(7), token.RemainQuota)
}

func TestPostConsumeRollsBackWhenTokenQuotaIsInsufficient(t *testing.T) {
	db := setupQuotaTest(t)
	require.NoError(t, db.Model(&User{}).Where("id = ?", 1).Update("quota", 20).Error)

	require.Error(t, PostConsumeTokenQuota(1, 11))

	var user User
	var token Token
	require.NoError(t, db.First(&user, 1).Error)
	require.NoError(t, db.First(&token, 1).Error)
	require.Equal(t, int64(20), user.Quota)
	require.Equal(t, int64(10), token.RemainQuota)
}
