package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/bot"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const maxOutboundMediaBytes = 25 * 1024 * 1024

// sendMediaURLs uploads each OutboundMessage.MediaURLs entry and sends it as an
// image/file message. Refs are resolved under a strict, off-by-default policy:
// only absolute local paths contained in a configured root are accepted (see
// readOutboundFile). Anything rejected is skipped with a warning. URL media is
// intentionally not fetched here — pulling a caller-supplied URL from inside the
// gateway is an SSRF sink with no safe static-analysis story; the /send caller
// should stage remote media to an allow-listed root instead.
func (a *adapter) sendMediaURLs(ctx context.Context, msg bot.OutboundMessage) (bot.SendResult, error) {
	var result bot.SendResult
	var firstErr error
	for _, ref := range msg.MediaURLs {
		res, err := a.sendOneMedia(ctx, msg, ref)
		if err != nil {
			a.logger.Warn("feishu media send rejected or failed", "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		result = res
	}
	return result, firstErr
}

func (a *adapter) sendOneMedia(ctx context.Context, msg bot.OutboundMessage, ref string) (bot.SendResult, error) {
	data, name, err := a.readOutboundFile(ref)
	if err != nil {
		return bot.SendResult{}, err
	}
	mimeType := http.DetectContentType(data[:min(len(data), 512)])
	if strings.HasPrefix(mimeType, "image/") {
		imageKey, err := a.uploadImage(ctx, data)
		if err == nil {
			content, _ := json.Marshal(map[string]string{"image_key": imageKey})
			return a.sendSDKContent(ctx, msg, larkim.MsgTypeImage, string(content))
		}
		a.logger.Warn("feishu image upload failed; falling back to file", "err", err)
	}
	fileKey, err := a.uploadFile(ctx, name, data)
	if err != nil {
		return bot.SendResult{}, err
	}
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	return a.sendSDKContent(ctx, msg, larkim.MsgTypeFile, string(content))
}

// readOutboundFile reads a media file for outbound sending under a strict
// policy: the ref is reduced to a bare filename and looked up directly inside a
// configured OutboundMediaRoots directory (empty by default → disabled). Using
// filepath.Base strips every directory component — including any traversal — so
// the file can only ever come from directly within a root, which is both the
// intended contract ("stage the file into a media root, then send it by name")
// and a path-injection sanitizer. A symlink sitting in a root that resolves
// outside it is rejected before any read.
func (a *adapter) readOutboundFile(ref string) ([]byte, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", fmt.Errorf("feishu outbound media: empty ref")
	}
	if len(a.cfg.OutboundMediaRoots) == 0 {
		return nil, "", fmt.Errorf("feishu outbound media: local file sending is disabled (set outbound_media_roots)")
	}
	// Reject any traversal outright, then reduce to a bare filename so no
	// directory component can survive into the lookup below.
	if strings.Contains(ref, "..") {
		return nil, "", fmt.Errorf("feishu outbound media: file name may not contain '..'")
	}
	name := filepath.Base(filepath.Clean(ref))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, "", fmt.Errorf("feishu outbound media: invalid file name")
	}
	for _, root := range a.cfg.OutboundMediaRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		full := filepath.Join(root, name)
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// Defense in depth: a regular file inside the root could still be a
		// symlink resolving outside it — reject that before reading.
		if resolved, err := filepath.EvalSymlinks(full); err == nil {
			if rootResolved, err2 := filepath.EvalSymlinks(root); err2 == nil {
				if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
					return nil, "", fmt.Errorf("feishu outbound media: %q escapes its root via a symlink", name)
				}
			}
		}
		if info.Size() == 0 || info.Size() > maxOutboundMediaBytes {
			return nil, "", fmt.Errorf("feishu outbound media: %q must be between 1 byte and 25 MB", name)
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return nil, "", err
		}
		return raw, name, nil
	}
	return nil, "", fmt.Errorf("feishu outbound media: %q not found in any configured root", name)
}

func (a *adapter) uploadImage(ctx context.Context, data []byte) (string, error) {
	client, err := a.sdkClient()
	if err != nil {
		return "", err
	}
	var key string
	err = withTransientRetry(ctx, a.logger, "upload image", func(ctx context.Context) error {
		req := larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType(larkim.CreateImageImageTypeMessage).
				Image(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.Image.Create(ctx, req)
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("feishu image upload error: empty response")
		}
		if !resp.Success() {
			return fmt.Errorf("feishu image upload error: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		if resp.Data == nil || resp.Data.ImageKey == nil {
			return fmt.Errorf("feishu image upload error: missing image key")
		}
		key = *resp.Data.ImageKey
		return nil
	})
	return key, err
}

func (a *adapter) uploadFile(ctx context.Context, name string, data []byte) (string, error) {
	client, err := a.sdkClient()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		name = "media.bin"
	}
	var key string
	err = withTransientRetry(ctx, a.logger, "upload file", func(ctx context.Context) error {
		req := larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(feishuFileType(name)).
				FileName(name).
				File(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.File.Create(ctx, req)
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("feishu file upload error: empty response")
		}
		if !resp.Success() {
			return fmt.Errorf("feishu file upload error: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		if resp.Data == nil || resp.Data.FileKey == nil {
			return fmt.Errorf("feishu file upload error: missing file key")
		}
		key = *resp.Data.FileKey
		return nil
	})
	return key, err
}

func feishuFileType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".mp4":
		return "mp4"
	case ".opus":
		return "opus"
	default:
		return "stream"
	}
}
