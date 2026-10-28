package handler

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/im/whatsapp"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

// whatsappPairing runs WhatsApp QR pairing sessions. Set once at startup by
// the container, like WeChat's qrCodeService singleton.
var whatsappPairing *whatsapp.PairingManager

// SetWhatsAppPairingManager installs the pairing manager used by the
// WhatsApp pairing routes.
func SetWhatsAppPairingManager(m *whatsapp.PairingManager) {
	whatsappPairing = m
}

// WhatsAppStartPairing starts a QR pairing session for a new WhatsApp device.
// POST /api/v1/whatsapp/pair
//
// WhatsAppStartPairing godoc
// @Summary      开始 WhatsApp 扫码配对
// @Description  创建一个新的 WhatsApp 关联设备配对会话，返回首个二维码内容（无请求体）
// @Tags         IM 渠道
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "配对会话（pairing_id + qr_code）"
// @Failure      429  {object}  map[string]interface{}  "进行中的配对过多"
// @Failure      500  {object}  map[string]interface{}  "配对启动失败"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /whatsapp/pair [post]
func (h *IMHandler) WhatsAppStartPairing(c *gin.Context) {
	ctx := c.Request.Context()
	if whatsappPairing == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "whatsapp pairing is not available"})
		return
	}

	id, result, err := whatsappPairing.Start(ctx)
	if err != nil {
		if errors.Is(err, whatsapp.ErrTooManyPairings) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		logger.Errorf(ctx, "[WhatsApp] Failed to start pairing: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start pairing: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": pairingResponse(id, result)})
}

// WhatsAppPairingStatus returns the state of a WhatsApp pairing session.
// POST /api/v1/whatsapp/pair/status
//
// WhatsAppPairingStatus godoc
// @Summary      轮询 WhatsApp 配对状态
// @Description  查询配对会话状态（pending/success/timeout/error）；pending 时返回当前二维码，success 时返回 device_jid
// @Tags         IM 渠道
// @Accept       json
// @Produce      json
// @Param        request  body      map[string]interface{}  true  "{pairing_id: string}"
// @Success      200      {object}  map[string]interface{}  "配对状态"
// @Failure      400      {object}  map[string]interface{}  "请求参数错误"
// @Failure      404      {object}  map[string]interface{}  "配对会话不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /whatsapp/pair/status [post]
func (h *IMHandler) WhatsAppPairingStatus(c *gin.Context) {
	if whatsappPairing == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "whatsapp pairing is not available"})
		return
	}

	var req struct {
		PairingID string `json:"pairing_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pairing_id is required"})
		return
	}

	result, ok := whatsappPairing.Status(req.PairingID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pairing session not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": pairingResponse(req.PairingID, result)})
}

func pairingResponse(id string, r whatsapp.PairingResult) gin.H {
	resp := gin.H{
		"pairing_id": id,
		"status":     r.Status,
	}
	if r.QRCode != "" {
		resp["qr_code"] = r.QRCode
	}
	if r.DeviceJID != "" {
		resp["device_jid"] = r.DeviceJID
	}
	if r.Error != "" {
		resp["error"] = r.Error
	}
	return resp
}
