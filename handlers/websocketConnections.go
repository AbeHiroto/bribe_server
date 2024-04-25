package handlers

import (
	"context"
	"net/http"

	"xicserver/bribe"
	"xicserver/bribe/actions"

	"xicserver/bribe/connection"
	"xicserver/bribe/database"
	"xicserver/models"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

// WebSocket接続へのアップグレードとセッションIDやゲームインスタンスの管理を行う
func WebSocketConnections(ctx context.Context, w http.ResponseWriter, r *http.Request, db *gorm.DB, rdb *redis.Client, logger *zap.Logger, clients map[*models.Client]bool, games map[uint]*models.Game, upgrader websocket.Upgrader) {
	sessionID := r.Header.Get("SessionID")
	var client *models.Client
	// リクエストヘッダーにセッションIDがある場合はセッションの復旧を行い、無ければ新規発行
	if sessionID != "" {
		client = database.ValidateSessionID(ctx, r, rdb, sessionID, logger)
		if client == nil {
			logger.Warn("Session ID is invalid or expired, creating a new session")
			client = connection.CreateNewSession(ctx, r, db, rdb, logger)
		}
	} else {
		client = connection.CreateNewSession(ctx, r, db, rdb, logger)
	}

	if client == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// WebSocket接続へのアップグレードと確立
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// WebSocket接続のアップグレードに失敗
		logger.Error("Error upgrading WebSocket", zap.Error(err))
		http.Error(w, "Error upgrading WebSocket", http.StatusInternalServerError)
		return
	}

	// クライアントリストに新規クライアントを追加
	clients[client] = true
	logger.Info("New client added", zap.Uint("UserID", client.UserID), zap.Uint("RoomID", client.RoomID), zap.String("Role", client.Role))

	// WebSocketのCloseHandlerを設定
	client.Conn.SetCloseHandler(func(code int, text string) error {
		// Closeイベントが発生した時の処理
		logger.Info("WebSocket closed", zap.Int("code", code), zap.String("reason", text))
		client.Conn.Close()     // 念のため、接続を閉じる
		delete(clients, client) // クライアントリストから削除
		return nil
	})

	// 乱数生成器のインスタンスを生成
	randGen := bribe.CreateLocalRandGenerator()

	// ゲームインスタンスの管理
	_, err = connection.ManageGameInstance(ctx, db, logger, games, client, conn)
	if err != nil {
		http.Error(w, "Failed to manage game instance", http.StatusInternalServerError)
		return
	}

	// クライアントごとにメッセージ読み取りゴルーチンを起動（）
	go actions.HandleClient(client, clients, games, randGen, db, logger)

	// Ping/Pongを管理するゴルーチンを起動
	go connection.MaintainWebSocketConnection(client, clients, logger)
}