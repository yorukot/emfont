package objectstore

import (
	"context"
	"io"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
)

type Unavailable struct{}

func (Unavailable) StatObject(context.Context, string, string) (appfont.ObjectInfo, error) {
	return appfont.ObjectInfo{}, appfont.ErrObjectStorageUnavailable
}

func (Unavailable) OpenObject(context.Context, string, string) (io.ReadCloser, appfont.ObjectInfo, error) {
	return nil, appfont.ObjectInfo{}, appfont.ErrObjectStorageUnavailable
}

func (Unavailable) PutObject(context.Context, string, io.Reader, int64, appfont.PutObjectOptions) (appfont.ObjectInfo, error) {
	return appfont.ObjectInfo{}, appfont.ErrObjectStorageUnavailable
}

func (Unavailable) PublicURL(context.Context, string, string) (string, error) {
	return "", appfont.ErrObjectStorageUnavailable
}
