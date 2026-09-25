package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestChannelPinRequiresAdministratorAndHyphenatedTokenWorks(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	model.DB = db
	t.Cleanup(func() { model.DB, common.RedisEnabled = previousDB, previousRedis })
	require.NoError(t, db.Create(&model.User{Id: 1, Username: "regular", AccessToken: "regular-access", AffCode: "regular", Role: model.RoleCommonUser, Status: model.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.User{Id: 2, Username: "admin", AccessToken: "admin-access", AffCode: "admin", Role: model.RoleAdminUser, Status: model.UserStatusEnabled}).Error)
	require.NoError(t, db.Create(&model.Token{UserId: 1, Key: "root-2025", Status: model.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	require.NoError(t, db.Create(&model.Token{UserId: 2, Key: "admin-key", Status: model.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	router := gin.New()
	router.GET("/v1/oneapi/proxy/:channelid/*target", TokenAuth(), func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(ctxkey.SpecificChannelId))
	})
	router.GET("/v1/test", TokenAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })

	request := func(path, token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer sk-"+token)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	require.Equal(t, http.StatusOK, request("/v1/test", "root-2025").Code)
	require.Equal(t, http.StatusForbidden, request("/v1/oneapi/proxy/12/chat", "root-2025").Code)
	admin := request("/v1/oneapi/proxy/12/chat", "admin-key")
	require.Equal(t, http.StatusOK, admin.Code)
	require.Equal(t, "12", admin.Body.String())
}
