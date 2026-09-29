package billing

import (
	"context"
	"testing"

	"github.com/infinmalum/one-gateway/common"
	"github.com/infinmalum/one-gateway/common/config"
	"github.com/infinmalum/one-gateway/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestReservationIsFinalizedOnce(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedis, previousMemoryCache, previousSQLite := common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite
	db, err := gorm.Open(sqlite.Open("file:billing-reservation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.User{}, &model.Token{}); err != nil {
		t.Fatal(err)
	}
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = false, false, true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, config.MemoryCacheEnabled, common.UsingSQLite = previousRedis, previousMemoryCache, previousSQLite
		_ = sqlDB.Close()
	})
	user := model.User{Username: "reservation", Status: model.UserStatusEnabled, Quota: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := model.Token{UserId: user.Id, Key: "reservation-key", Status: model.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	reservation, err := Reserve(ctx, user.Id, token.Id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if charged := reservation.Settle(ctx, 25); charged != 25 {
		t.Fatalf("charged %d instead of 25", charged)
	}
	if charged := reservation.Settle(ctx, 25); charged != 25 {
		t.Fatalf("repeated settlement returned %d", charged)
	}
	reservation.Refund(ctx)
	refundable, err := Reserve(ctx, user.Id, token.Id, 10)
	if err != nil {
		t.Fatal(err)
	}
	refundable.Refund(ctx)
	refundable.Refund(ctx)
	if quota, err := model.GetUserQuota(user.Id); err != nil || quota != 75 {
		t.Fatalf("reservation settled more than once: user quota %d, error %v", quota, err)
	}
	var stored model.Token
	if err := db.First(&stored, token.Id).Error; err != nil || stored.RemainQuota != 75 {
		t.Fatalf("token quota differs from user quota: %+v, error %v", stored, err)
	}
}
