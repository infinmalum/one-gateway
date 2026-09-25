package image_test

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
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

		})
	}
}

func TestPrivateImageURLIsRejected(t *testing.T) {
	client.Init()
	_, _, err := img.GetImageFromUrl("http://127.0.0.1/private.png")
	require.Error(t, err)
	_, _, err = img.GetImageFromUrl("http://[::1]/private.png")
	require.Error(t, err)
}
