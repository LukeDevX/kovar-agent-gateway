package kovarmodel

import (
	"bytes"
	"image"
	"image/png"
	"mime"
	"strings"

	"kovar-gateway/internal/platform/httpx"
)

func validateUploads(r Request) error {
	if len(r.Files) > 0 && r.Operation != "image_edit" && r.Operation != "transcription" {
		return httpx.Invalid("files are not supported for this operation")
	}
	if r.Operation == "image_edit" {
		original, ok := r.Files["image"]
		if !ok {
			return httpx.Invalid("image edit requires an uploaded image")
		}
		size, err := pngSize(original)
		if err != nil {
			return err
		}
		if mask, ok := r.Files["mask"]; ok {
			maskSize, err := pngSize(mask)
			if err != nil {
				return err
			}
			if size.Width != maskSize.Width || size.Height != maskSize.Height {
				return httpx.Invalid("mask dimensions must match image")
			}
		}
	}
	if r.Operation == "transcription" {
		if _, ok := r.Files["file"]; !ok {
			return httpx.Invalid("transcription requires an uploaded file")
		}
	}
	for field, file := range r.Files {
		if r.Operation == "image_edit" && field != "image" && field != "mask" || r.Operation == "transcription" && field != "file" {
			return httpx.Invalid("unsupported uploaded field")
		}
		if _, exists := r.Payload[field]; exists {
			return httpx.Invalid("file fields cannot also occur in payload")
		}
		if len(file.Data) == 0 || len(file.Data) > 8<<20 {
			return httpx.Invalid("file must be 1 byte to 8 MiB")
		}
		if file.Name == "" || len(file.Name) > 255 || strings.ContainsAny(field+file.Name, "\r\n\"\\/") {
			return httpx.Invalid("invalid uploaded filename")
		}
		if file.ContentType != "" {
			if _, _, err := mime.ParseMediaType(file.ContentType); err != nil || strings.ContainsAny(file.ContentType, "\r\n") {
				return httpx.Invalid("invalid upload content type")
			}
		}
	}
	return nil
}
func pngSize(file File) (image.Config, error) {
	if len(file.Data) >= 4<<20 {
		return image.Config{}, httpx.Invalid("image edit PNG must be smaller than 4 MiB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(file.Data))
	if err != nil || cfg.Width != cfg.Height || cfg.Width < 1 || cfg.Width > 4096 {
		return image.Config{}, httpx.Invalid("image edit requires a square PNG, at most 4096 pixels per side")
	}
	return cfg, nil
}
