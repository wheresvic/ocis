package svc

import (
	"context"
	"fmt"

	"gopkg.in/square/go-jose.v2/jwt"

	"github.com/owncloud/ocis-pkg/v2/log"
	v0proto "github.com/owncloud/ocis-thumbnails/pkg/proto/v0"
	"github.com/owncloud/ocis-thumbnails/pkg/thumbnail"
	"github.com/owncloud/ocis-thumbnails/pkg/thumbnail/imgsource"
	"github.com/owncloud/ocis-thumbnails/pkg/thumbnail/resolution"
	"github.com/pkg/errors"
)

// NewService returns a service implementation for Service.
func NewService(opts ...Option) v0proto.ThumbnailServiceHandler {
	options := newOptions(opts...)
	logger := options.Logger
	resolutions, err := resolution.New(options.Config.Thumbnail.Resolutions)
	if err != nil {
		logger.Fatal().Err(err).Msg("resolutions not configured correctly")
	}
	svc := Thumbnail{
		manager: thumbnail.NewSimpleManager(
			options.ThumbnailStorage,
			logger,
		),
		resolutions: resolutions,
		source:      options.ImageSource,
		logger:      logger,
	}

	return svc
}

// Thumbnail implements the GRPC handler.
type Thumbnail struct {
	manager     thumbnail.Manager
	resolutions resolution.Resolutions
	source      imgsource.Source
	logger      log.Logger
}

// GetThumbnail retrieves a thumbnail for an image
func (g Thumbnail) GetThumbnail(ctx context.Context, req *v0proto.GetRequest, rsp *v0proto.GetResponse) error {
	encoder := thumbnail.EncoderForType(req.Filetype.String())
	if encoder == nil {
		return fmt.Errorf("can't be encoded. filetype %s not supported", req.Filetype.String())
	}
	r := g.resolutions.ClosestMatch(int(req.Width), int(req.Height))

	auth := req.Authorization
	if auth == "" {
		return fmt.Errorf("authorization is missing")
	}
	username, err := usernameFromAuthorization(auth)
	if err != nil {
		return err
	}

	tr := thumbnail.Request{
		Resolution: r,
		ImagePath:  req.Filepath,
		Encoder:    encoder,
		ETag:       req.Etag,
		Username:   username,
	}

	thumbnail := g.manager.GetStored(tr)
	if thumbnail != nil {
		rsp.Thumbnail = thumbnail
		rsp.Mimetype = tr.Encoder.MimeType()
		return nil
	}

	sCtx := imgsource.WithAuthorization(ctx, auth)
	img, err := g.source.Get(sCtx, tr.ImagePath)
	if err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("could not retrieve image")
	}
	thumbnail, err = g.manager.Get(tr, img)
	if err != nil {
		return err
	}

	rsp.Thumbnail = thumbnail
	rsp.Mimetype = tr.Encoder.MimeType()
	return nil
}

func usernameFromAuthorization(auth string) (string, error) {
	tokenString := auth[len("Bearer "):] // strip the bearer prefix

	var claims map[string]interface{}
	token, err := jwt.ParseSigned(tokenString)
	if err != nil {
		return "", errors.Wrap(err, "could not parse auth token")
	}
	err = token.UnsafeClaimsWithoutVerification(&claims)
	if err != nil {
		return "", errors.Wrap(err, "could not get claims from auth token")
	}

	identityMap := claims["kc.identity"].(map[string]interface{})
	username := identityMap["kc.i.un"].(string)

	return username, nil
}
