package image_test

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/client"
	img "github.com/songquanpeng/one-api/common/image"
	"github.com/stretchr/testify/require"
	_ "golang.org/x/image/webp"
)

func TestImageFormats(t *testing.T) {
	client.Init()
	formats := []struct {
		name        string
		extension   string
		contentType string
	}{
		{"jpeg", "jpg", "image/jpeg"},
		{"png", "png", "image/png"},
		{"gif", "gif", "image/gif"},
		{"webp", "webp", "image/webp"},
	}

	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "sample."+format.extension))
			require.NoError(t, err)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", format.contentType)
				if r.Method != http.MethodHead {
					_, _ = w.Write(data)
				}
			}))
			defer server.Close()

			decoded, name, err := image.Decode(bytes.NewReader(data))
			require.NoError(t, err)
			require.Equal(t, format.name, name)
			require.Equal(t, image.Pt(2, 3), decoded.Bounds().Size())

			config, configName, err := image.DecodeConfig(bytes.NewReader(data))
			require.NoError(t, err)
			require.Equal(t, format.name, configName)
			require.Equal(t, 2, config.Width)
			require.Equal(t, 3, config.Height)

			encoded := base64.StdEncoding.EncodeToString(data)
			width, height, err := img.GetImageSizeFromBase64(encoded)
			require.NoError(t, err)
			require.Equal(t, 2, width)
			require.Equal(t, 3, height)

			width, height, err = img.GetImageSize("data:" + format.contentType + ";base64," + encoded)
			require.NoError(t, err)
			require.Equal(t, 2, width)
			require.Equal(t, 3, height)

			width, height, err = img.GetImageSize(strings.TrimSuffix(server.URL, "/") + "/sample")
			require.NoError(t, err)
			require.Equal(t, 2, width)
			require.Equal(t, 3, height)
		})
	}
}
