package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/redis/go-redis/v9"
	"github.com/silenceper/wechat/v2"
	"github.com/silenceper/wechat/v2/cache"
	wxconfig "github.com/silenceper/wechat/v2/officialaccount/config"
	"github.com/silenceper/wechat/v2/officialaccount/message"
	"protobuf/internal/config"
	"protobuf/internal/core"
	"protobuf/internal/model"
)

type Manager struct {
	Config config.Config
	Core   *core.Service
	Redis  *redis.Client
	HTTP   *http.Client
}
type SendError struct {
	Message   string
	Retryable bool
	Unknown   bool
}

func (e *SendError) Error() string { return e.Message }
func New(c config.Config, s *core.Service, r *redis.Client) *Manager {
	return &Manager{Config: c, Core: s, Redis: r, HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (m *Manager) FeishuWebhook(w http.ResponseWriter, r *http.Request) {
	if m.Config.FeishuAppID == "" {
		http.Error(w, "Feishu is not configured", 503)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	var encrypted struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &encrypted) != nil || encrypted.Encrypt == "" {
		http.Error(w, "encrypted payload required", 400)
		return
	}
	plain, err := larkevent.EventDecrypt(encrypted.Encrypt, m.Config.FeishuEncryptKey)
	if err != nil {
		http.Error(w, "invalid encrypted payload", 401)
		return
	}
	var e struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Token     string `json:"token"`
		Header    struct {
			EventID   string `json:"event_id"`
			AppID     string `json:"app_id"`
			Token     string `json:"token"`
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
				SenderType string `json:"sender_type"`
			} `json:"sender"`
			Message struct {
				MessageID   string `json:"message_id"`
				ChatType    string `json:"chat_type"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"`
			} `json:"message"`
		} `json:"event"`
	}
	if json.Unmarshal(plain, &e) != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if e.Type == "url_verification" {
		if subtle.ConstantTimeCompare([]byte(e.Token), []byte(m.Config.FeishuToken)) != 1 {
			http.Error(w, "invalid token", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"challenge": e.Challenge})
		return
	}
	signature := sha256.Sum256(append([]byte(r.Header.Get("X-Lark-Request-Timestamp")+r.Header.Get("X-Lark-Request-Nonce")+m.Config.FeishuEncryptKey), body...))
	if r.Header.Get("X-Lark-Request-Timestamp") == "" || r.Header.Get("X-Lark-Request-Nonce") == "" || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(signature[:])), []byte(r.Header.Get("X-Lark-Signature"))) != 1 || e.Header.AppID != m.Config.FeishuAppID || subtle.ConstantTimeCompare([]byte(e.Header.Token), []byte(m.Config.FeishuToken)) != 1 {
		http.Error(w, "invalid signature or application", 401)
		return
	}
	if e.Header.EventType != "im.message.receive_v1" || e.Event.Sender.SenderType != "user" {
		w.WriteHeader(200)
		return
	}
	// Personal data stays in private conversations. Shared workspaces are managed in the web UI.
	if e.Event.Message.ChatType != "p2p" || e.Event.Message.MessageType != "text" {
		w.WriteHeader(200)
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(e.Event.Message.Content), &content) != nil {
		http.Error(w, "invalid text", 400)
		return
	}
	err = m.Core.Receive(r.Context(), model.InboxEvent{Provider: "feishu", AppID: m.Config.FeishuAppID, ExternalID: e.Event.Message.MessageID, SenderID: e.Event.Sender.SenderID.OpenID, Text: content.Text})
	if err != nil {
		http.Error(w, "event persistence failed", 503)
		return
	}
	w.WriteHeader(200)
}

type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(n int) {
	if b.status == 0 {
		b.status = n
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	return b.body.Write(p)
}
func (m *Manager) WechatWebhook(w http.ResponseWriter, r *http.Request) {
	if m.Config.WechatAppID == "" {
		http.Error(w, "WeChat is not configured", 503)
		return
	}
	if m.Config.WechatAESKey != "" && r.Method == http.MethodPost && r.URL.Query().Get("encrypt_type") != "aes" {
		http.Error(w, "encrypted payload required", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	account := wechat.NewWechat().GetOfficialAccount(&wxconfig.Config{AppID: m.Config.WechatAppID, AppSecret: m.Config.WechatSecret, Token: m.Config.WechatToken, EncodingAESKey: m.Config.WechatAESKey, Cache: cache.NewMemory()})
	buffer := &bufferedResponse{header: make(http.Header)}
	server := account.GetServer(r, buffer)
	var persistErr error
	server.SetMessageHandler(func(msg *message.MixMessage) *message.Reply {
		if msg.MsgType != message.MsgTypeText {
			return nil
		}
		persistErr = m.Core.Receive(r.Context(), model.InboxEvent{Provider: "wechat", AppID: m.Config.WechatAppID, ExternalID: strconv.FormatInt(msg.MsgID, 10), SenderID: string(msg.FromUserName), Text: msg.Content})
		return nil
	})
	if err := server.Serve(); err != nil {
		http.Error(w, "invalid WeChat request", 401)
		return
	}
	if persistErr != nil {
		http.Error(w, "event persistence failed", 503)
		return
	}
	if r.Method == http.MethodPost {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "success")
		return
	}
	for k, v := range buffer.header {
		w.Header()[k] = v
	}
	if buffer.status == 0 {
		buffer.status = 200
	}
	w.WriteHeader(buffer.status)
	_, _ = w.Write(buffer.body.Bytes())
}

func (m *Manager) token(ctx context.Context, provider string) (string, error) {
	app := m.Config.FeishuAppID
	if provider == "wechat" {
		app = m.Config.WechatAppID
	}
	if app == "" {
		return "", &SendError{Message: provider + " is not configured"}
	}
	key := "assistant:token:" + provider + ":" + app
	lockKey := key + ":lock"
	lockValue := core.Secret()
	for {
		if value, err := m.Redis.Get(ctx, key).Result(); err == nil {
			return value, nil
		} else if err != redis.Nil {
			return "", err
		}
		got, err := m.Redis.SetNX(ctx, lockKey, lockValue, 12*time.Second).Result()
		if err != nil {
			return "", err
		}
		if got {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
	defer m.Redis.Eval(context.WithoutCancel(ctx), "if redis.call('get',KEYS[1]) == ARGV[1] then return redis.call('del',KEYS[1]) end return 0", []string{lockKey}, lockValue)
	var raw struct {
		Code              int    `json:"code"`
		ErrCode           int    `json:"errcode"`
		TenantAccessToken string `json:"tenant_access_token"`
		AccessToken       string `json:"access_token"`
		Expire            int    `json:"expire"`
		ExpiresIn         int    `json:"expires_in"`
	}
	endpoint := "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"
	method := http.MethodPost
	var payload any = map[string]string{"app_id": m.Config.FeishuAppID, "app_secret": m.Config.FeishuSecret}
	if provider == "wechat" {
		method = http.MethodGet
		payload = nil
		endpoint = "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=" + m.Config.WechatAppID + "&secret=" + m.Config.WechatSecret
	}
	if err := m.request(ctx, method, endpoint, "", payload, &raw); err != nil {
		return "", err
	}
	token, ttl := raw.TenantAccessToken, raw.Expire
	if provider == "wechat" {
		token, ttl = raw.AccessToken, raw.ExpiresIn
	}
	if token == "" {
		return "", &SendError{Message: "platform token request rejected", Retryable: raw.Code == 99991400 || raw.ErrCode == -1}
	}
	lifetime := time.Duration(ttl-120) * time.Second
	if lifetime < 30*time.Second {
		lifetime = 30 * time.Second
	}
	if err := m.Redis.Set(ctx, key, token, lifetime).Err(); err != nil {
		return "", err
	}
	return token, nil
}

func (m *Manager) request(ctx context.Context, method, endpoint, token string, payload, output any) error {
	var data []byte
	var err error
	if payload != nil {
		data, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return &SendError{Message: "platform network request failed", Retryable: true, Unknown: method == http.MethodPost && strings.Contains(endpoint, "messages") || method == http.MethodPost && strings.Contains(endpoint, "message/")}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &SendError{Message: fmt.Sprintf("platform HTTP %d", resp.StatusCode), Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500, Unknown: resp.StatusCode >= 500 && strings.Contains(endpoint, "message")}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return &SendError{Message: "invalid platform response", Retryable: true, Unknown: strings.Contains(endpoint, "message")}
	}
	return nil
}

func (m *Manager) Send(ctx context.Context, provider, target, body, kind, key string) (string, error) {
	token, err := m.token(ctx, provider)
	if err != nil {
		return "", err
	}
	if provider == "feishu" {
		content, _ := json.Marshal(map[string]string{"text": body})
		payload := map[string]any{"receive_id": target, "msg_type": "text", "content": string(content), "uuid": key}
		var result struct {
			Code int `json:"code"`
			Data struct {
				MessageID string `json:"message_id"`
			} `json:"data"`
		}
		if err = m.request(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id", token, payload, &result); err != nil {
			return "", err
		}
		if result.Code != 0 {
			if result.Code == 99991663 || result.Code == 99991668 {
				m.Redis.Del(ctx, "assistant:token:feishu:"+m.Config.FeishuAppID)
			}
			return "", &SendError{Message: fmt.Sprintf("Feishu code %d", result.Code), Retryable: result.Code == 99991400 || result.Code == 230020 || result.Code == 99991663 || result.Code == 99991668}
		}
		return result.Data.MessageID, nil
	}
	if provider != "wechat" {
		return "", &SendError{Message: "unknown provider"}
	}
	endpoint := "https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=" + token
	var payload any = map[string]any{"touser": target, "msgtype": "text", "text": map[string]string{"content": body}}
	if kind == "reminder" {
		if m.Config.WechatTemplateID == "" {
			return "", &SendError{Message: "微信主动提醒未配置获准模板；通知已保留在站内"}
		}
		summary := []rune(body)
		if len(summary) > 20 {
			summary = summary[:20]
		}
		endpoint = "https://api.weixin.qq.com/cgi-bin/message/template/send?access_token=" + token
		payload = map[string]any{"touser": target, "template_id": m.Config.WechatTemplateID, "data": map[string]any{m.Config.WechatTemplateField: map[string]string{"value": string(summary)}}}
	}
	var result struct {
		ErrCode int   `json:"errcode"`
		MsgID   int64 `json:"msgid"`
	}
	if err = m.request(ctx, http.MethodPost, endpoint, "", payload, &result); err != nil {
		return "", err
	}
	if result.ErrCode != 0 {
		expired := result.ErrCode == 40001 || result.ErrCode == 42001
		if expired {
			m.Redis.Del(ctx, "assistant:token:wechat:"+m.Config.WechatAppID)
		}
		return "", &SendError{Message: fmt.Sprintf("WeChat code %d", result.ErrCode), Retryable: expired || result.ErrCode == -1 || result.ErrCode == 45009}
	}
	return strconv.FormatInt(result.MsgID, 10), nil
}
