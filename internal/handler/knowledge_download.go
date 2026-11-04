package handler

import (
	"archive/zip"
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/filetransport"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const (
	maxBatchDownloadFiles             = 200
	maxBatchDownloadBytes       int64 = 512 * 1024 * 1024
	maxConcurrentBatchDownloads       = 4
)

var batchDownloadSlots = make(chan struct{}, maxConcurrentBatchDownloads)

// BatchDownloadKnowledgeRequest 指定同一知识库中需要下载的文档。
type BatchDownloadKnowledgeRequest struct {
	IDs []string `json:"ids" binding:"required,min=1,max=200,dive,required,max=128"`
}

type knowledgeDownloadEntry struct {
	ID         string
	FolderPath string
	// Watermark is the mark planned for this entry. nil means the document
	// needs none (public and not forced).
	Watermark *types.WatermarkApplied
}

// BatchDownloadKnowledge godoc
// @Summary 批量下载知识文件
// @Description 将同一知识库的最多 200 个文档打包为 ZIP，原始内容合计不超过 512 MiB。无原文件的条目会被跳过；无权访问、跨库或读取失败时不返回残缺压缩包。
// @Tags 知识管理
// @Accept json
// @Produce application/zip
// @Param id path string true "知识库ID"
// @Param request body BatchDownloadKnowledgeRequest true "文档ID列表"
// @Success 200 {file} file "ZIP 压缩包"
// @Failure 400 {object} errors.AppError
// @Failure 401 {object} errors.AppError
// @Failure 403 {object} errors.AppError
// @Failure 404 {object} errors.AppError
// @Failure 429 {object} errors.AppError
// @Failure 500 {object} errors.AppError
// @Security Bearer
// @Security ApiKeyAuth
// @Router /knowledge-bases/{id}/knowledge/batch-download [post]
func (h *KnowledgeHandler) BatchDownloadKnowledge(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	var req BatchDownloadKnowledgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(errors.NewBadRequestError("请选择 1～200 个文档进行批量下载"))
		return
	}

	kbID := c.Param("id")
	grant, err := resolveHandlerKBAccessFor(
		c, kbID, h.kbService, h.kbShareService, h.agentShareService, types.OrgRoleEditor,
	)
	if err != nil {
		_ = c.Error(err)
		return
	}
	tenantID := grant.EffectiveTenantID
	ctx := types.WithExecutionTenant(c.Request.Context(), tenantID)
	ids := uniqueKnowledgeDownloadIDs(req.IDs)
	if len(ids) == 0 {
		_ = c.Error(errors.NewBadRequestError("文档 ID 不能为空"))
		return
	}

	items, err := h.kgService.GetKnowledgeBatch(ctx, tenantID, ids)
	if err != nil {
		_ = c.Error(errors.NewInternalServerError("无法获取待下载文档，请稍后重试"))
		return
	}
	byID := make(map[string]*types.Knowledge, len(items))
	for _, item := range items {
		if item != nil && item.KnowledgeBaseID == kbID && item.TenantID == tenantID {
			byID[item.ID] = item
		}
	}
	entries := make([]knowledgeDownloadEntry, 0, len(ids))
	// 在读取任何文件之前检查整批文档，防止混入其他知识库或租户的 ID。
	for _, id := range ids {
		item := byID[id]
		if item == nil {
			_ = c.Error(errors.NewNotFoundError("部分文档不存在或不属于当前知识库，请刷新列表后重试"))
			return
		}
		if !item.IsManual() && item.FilePath == "" {
			continue
		}
		entry := knowledgeDownloadEntry{ID: id, FolderPath: item.FolderPath}
		// §16.3：批次里每一篇按自己的密级决定水印，而不是整批统一——把一份
		// internal 文档的水印强度套到同批的 secret 文档上会低估后者。
		if h.watermarks != nil {
			applied := h.watermarks.Plan(ctx, tenantID, callerUserID(ctx), item,
				downloadNameFor(item), "export", "")
			if applied.Strength != types.WatermarkStrengthNone {
				entry.Watermark = &applied
			}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		_ = c.Error(errors.NewBadRequestError("所选文档没有可下载的原始文件，请取消选择后重试"))
		return
	}

	if !tryAcquireBatchDownloadSlot() {
		_ = c.Error(errors.NewTooManyRequestsError("当前批量下载过多，请稍后重试"))
		return
	}
	defer releaseBatchDownloadSlot()

	// 先在临时文件中完整生成压缩包，避免读取失败时向用户返回残缺 ZIP。
	archive, err := os.CreateTemp("", "weknora-download-*.zip")
	if err != nil {
		_ = c.Error(errors.NewInternalServerError("无法创建下载压缩包，请稍后重试"))
		return
	}
	archivePath := archive.Name()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archivePath)
	}()
	if err := writeKnowledgeDownloadArchive(
		ctx, archive, entries, h.kgService.GetKnowledgeFile, maxBatchDownloadBytes,
	); err != nil {
		mapped := mapKnowledgeDownloadError(err)
		if appErr, ok := errors.IsAppError(mapped); !ok || appErr.HTTPCode >= 500 {
			logger.ErrorWithFields(ctx, err, nil)
		}
		_ = c.Error(mapped)
		return
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = c.Error(errors.NewInternalServerError("无法读取下载压缩包，请稍后重试"))
		return
	}
	filename := "knowledge-files-" + time.Now().Format("20060102-150405") + ".zip"
	if err := filetransport.Serve(c.Writer, c.Request, archive, filetransport.Options{
		Filename: filename, Download: true, ContentType: "application/zip", CacheControl: "private, no-store",
	}); err != nil {
		logger.Errorf(ctx, "Failed to send knowledge archive: %v", err)
	}
}

func tryAcquireBatchDownloadSlot() bool {
	select {
	case batchDownloadSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseBatchDownloadSlot() {
	<-batchDownloadSlots
}

func uniqueKnowledgeDownloadIDs(input []string) []string {
	ids := make([]string, 0, len(input))
	seen := make(map[string]bool, len(input))
	for _, id := range input {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func isKnowledgeDownloadCanceled(err error) bool {
	return err != nil && (stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded))
}

func mapKnowledgeDownloadError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.IsAppError(err); ok {
		return err
	}
	if isKnowledgeDownloadCanceled(err) {
		return errors.NewBadRequestError("下载已取消")
	}
	return err
}

type knowledgeDownloadOpener func(context.Context, string) (io.ReadCloser, string, error)

func writeKnowledgeDownloadArchive(
	ctx context.Context,
	output io.Writer,
	entries []knowledgeDownloadEntry,
	open knowledgeDownloadOpener,
	limit int64,
) error {
	writer := zip.NewWriter(output)
	usedNames := make(map[string]bool, len(entries))
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, filename, err := open(ctx, entry.ID)
		if err != nil {
			if isKnowledgeDownloadCanceled(err) {
				return err
			}
			return errors.NewInternalServerError("部分文件无法读取，未生成压缩包，请检查文档后重试").WithDetails(
				gin.H{"knowledge_id": entry.ID},
			)
		}
		name := uniqueKnowledgeDownloadZipPath(entry.FolderPath, filename, usedNames)
		// 原样存储可避免 PDF、Office 等已压缩文件再次压缩的 CPU 开销。
		zipEntry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			_ = file.Close()
			return errors.NewInternalServerError("无法写入下载压缩包，请稍后重试")
		}

		// §16.3 水印：可注入的格式先读进内存再写水印（这些是纯文本，
		// 体量远小于 limit）；其余格式原样写入，另加一份受控文件说明。
		var source io.Reader = file
		if entry.Watermark != nil && entry.Watermark.Mode == types.WatermarkInjected {
			raw, readErr := io.ReadAll(io.LimitReader(file, limit-total+1))
			_ = file.Close()
			if readErr != nil {
				if isKnowledgeDownloadCanceled(readErr) {
					return readErr
				}
				return errors.NewInternalServerError("部分文件读取失败，未生成压缩包，请稍后重试").WithDetails(
					gin.H{"knowledge_id": entry.ID},
				)
			}
			source = bytes.NewReader(types.InjectTextWatermark(raw, filename, *entry.Watermark))
		}

		n, copyErr := io.Copy(zipEntry, io.LimitReader(
			&knowledgeDownloadReader{ctx: ctx, reader: source}, limit-total+1,
		))
		closeErr := file.Close()
		total += n
		if total > limit {
			return errors.NewBadRequestError("所选文件合计超过 512 MiB，请分批下载")
		}
		if isKnowledgeDownloadCanceled(copyErr) {
			return copyErr
		}
		if copyErr != nil || closeErr != nil {
			return errors.NewInternalServerError("部分文件读取失败，未生成压缩包，请稍后重试").WithDetails(
				gin.H{"knowledge_id": entry.ID},
			)
		}
		// sidecar 说明：告诉接收方这份文件本身没有可见水印，以及为什么。
		if entry.Watermark != nil && entry.Watermark.SidecarName != "" {
			sidecarName := uniqueKnowledgeDownloadZipPath(entry.FolderPath,
				entry.Watermark.SidecarName, usedNames)
			sidecarEntry, sidecarErr := writer.CreateHeader(
				&zip.FileHeader{Name: sidecarName, Method: zip.Deflate})
			if sidecarErr != nil {
				return errors.NewInternalServerError("无法写入下载压缩包，请稍后重试")
			}
			if _, sidecarErr := io.WriteString(sidecarEntry, entry.Watermark.SidecarBody()); sidecarErr != nil {
				return errors.NewInternalServerError("无法写入下载压缩包，请稍后重试")
			}
		}
	}
	if err := writer.Close(); err != nil {
		return errors.NewInternalServerError("无法完成下载压缩包，请稍后重试")
	}
	return nil
}

// 每次读取前检查取消信号，用户离开页面后停止继续打包。
type knowledgeDownloadReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *knowledgeDownloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func uniqueKnowledgeDownloadName(filename string, used map[string]bool) string {
	return uniqueKnowledgeDownloadZipPath("", filename, used)
}

func uniqueKnowledgeDownloadZipPath(folderPath, filename string, used map[string]bool) string {
	base := sanitizeDownloadFileName(filename)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dir := sanitizeDownloadFolderPath(folderPath)
	name := base
	if dir != "" {
		name = dir + "/" + base
	}
	for index := 2; used[strings.ToLower(name)]; index++ {
		suffixed := fmt.Sprintf("%s (%d)%s", stem, index, ext)
		if dir != "" {
			name = dir + "/" + suffixed
		} else {
			name = suffixed
		}
	}
	used[strings.ToLower(name)] = true
	return name
}

func sanitizeDownloadFileName(filename string) string {
	filename = path.Base(strings.ReplaceAll(filename, "\\", "/"))
	filename = sanitizeDownloadRunes(filename)
	filename = strings.Trim(filename, " .")
	if filename == "" {
		filename = "document"
	}
	ext := path.Ext(filename)
	if len(ext) > 20 {
		ext = ""
	}
	stem := strings.TrimSuffix(filename, ext)
	stem = shortenDownloadName(stem, 180)
	stem = strings.TrimRight(stem, " .")
	if stem == "" {
		stem = "document"
	}
	return escapeWindowsReservedName(stem) + ext
}

func sanitizeDownloadFolderPath(folderPath string) string {
	folderPath = strings.ReplaceAll(folderPath, "\\", "/")
	var parts []string
	var total int
	for _, part := range strings.Split(folderPath, "/") {
		part = sanitizeDownloadRunes(part)
		part = strings.Trim(part, " .")
		if part == "" || part == "." || part == ".." {
			continue
		}
		part = shortenDownloadName(part, 80)
		part = strings.TrimRight(part, " .")
		if part == "" {
			continue
		}
		part = escapeWindowsReservedName(part)
		if total+len(part)+1 > 160 {
			break
		}
		parts = append(parts, part)
		total += len(part) + 1
	}
	return strings.Join(parts, "/")
}

func sanitizeDownloadRunes(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, value)
}

func shortenDownloadName(value string, limit int) string {
	var shortened strings.Builder
	for _, r := range value {
		if shortened.Len()+len(string(r)) > limit {
			break
		}
		shortened.WriteRune(r)
	}
	return shortened.String()
}

func escapeWindowsReservedName(stem string) string {
	reserved := strings.ToUpper(strings.Split(stem, ".")[0])
	reservedRunes := []rune(reserved)
	if reserved == "CON" || reserved == "PRN" || reserved == "AUX" || reserved == "NUL" ||
		(len(reservedRunes) == 4 && (strings.HasPrefix(reserved, "COM") || strings.HasPrefix(reserved, "LPT")) &&
			strings.ContainsRune("123456789¹²³", reservedRunes[3])) {
		return "_" + stem
	}
	return stem
}

// downloadNameFor renders the delivered file name used when planning a
// watermark. The real name is resolved by the file service; this only needs
// the extension, so the plan matches what will actually be sent.
func downloadNameFor(knowledge *types.Knowledge) string {
	if knowledge == nil {
		return ""
	}
	if knowledge.IsManual() {
		// Manual entries are delivered as Markdown (the service appends the
		// extension); mirror that here so the injection decision matches.
		return knowledge.Title + ".md"
	}
	if knowledge.FileName != "" {
		return knowledge.FileName
	}
	return knowledge.Title
}
